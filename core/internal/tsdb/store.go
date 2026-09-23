// Package tsdb is an embedded tiered time-series store. Tier 0 keeps
// full-resolution samples: an in-memory active block per series that is
// Gorilla-compressed into one file per block once it is full (or on
// capacity). Checkpoints preserve unfinished blocks in a single atomic image.
// Retention removes expired blocks and prunes unfinished samples.
package tsdb

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	blockMagic   = "MONB"
	blockVersion = 1
	// DefaultBlockSize is the number of samples per on-disk block (1h at 1s).
	DefaultBlockSize = 3600
	// Upper bounds used to reject corrupt headers before allocating.
	maxBlockSamples = 1 << 22
	maxBlockBytes   = 64 << 20
)

type Options struct {
	Dir           string
	Retention     time.Duration // 0 = keep forever
	RetentionSize int64         // bytes, 0 = unlimited
	BlockSize     int
	Checkpoint    time.Duration // flush active blocks this often (0 = only when full/close)
	Tiers         []TierSpec    // rollup tiers; nil = DefaultTiers(), empty = tier0 only
	Logger        *slog.Logger
}

type Point struct {
	TS    int64   `json:"t"`
	Value float64 `json:"v"`
}

type blockMeta struct {
	path  string
	start int64
	end   int64
	count int
	size  int64
}

type series struct {
	id     string
	mu     sync.Mutex
	ts     []int64
	vals   []float64
	blocks []blockMeta // sorted by start
	dirty  bool
	last   int64 // newest timestamp ever accepted (buffer or flushed block)
	// flushMu serialises flushes of one series so blocks land in time order
	// even when a checkpoint and a full-block flush race.
	flushMu sync.Mutex
}

type Store struct {
	checkpointMu    sync.RWMutex
	flushMu         sync.Mutex
	revision        atomic.Uint64
	flushedRevision uint64
	persistenceMu   sync.Mutex
	persistence     PersistenceStatus
	opt             Options
	log             *slog.Logger
	mu              sync.RWMutex
	series          map[string]*series
	tiers           []*tier
	stop            chan struct{}
	wg              sync.WaitGroup
	closed          bool
	wal             *walLog
	// afterSnapshot runs after a raw block is copied and before that copy is
	// removed from the active buffer. Tests use it to drop a prefix in that
	// window. Production leaves it nil.
	afterSnapshot func(*series)
}

func Open(opt Options) (*Store, error) {
	if opt.BlockSize <= 0 {
		opt.BlockSize = DefaultBlockSize
	}
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	tier0 := filepath.Join(opt.Dir, "tier0")
	if err := os.MkdirAll(tier0, 0o750); err != nil {
		return nil, err
	}
	s := &Store{opt: opt, log: opt.Logger, series: map[string]*series{}, stop: make(chan struct{})}
	if err := s.load(tier0); err != nil {
		return nil, err
	}
	specs := opt.Tiers
	if specs == nil {
		specs = DefaultTiers()
	}
	for i, spec := range specs {
		t, err := newTier(i+1, spec, opt.Dir)
		if err != nil {
			return nil, err
		}
		n, err := t.load()
		if err != nil {
			return nil, err
		}
		s.log.Info("tsdb: tier loaded", "tier", i+1, "every", spec.Every, "series", len(t.series), "blocks", n)
		s.tiers = append(s.tiers, t)
	}
	if err := s.loadCheckpoint(); err != nil {
		return nil, err
	}
	if err := s.replayWAL(); err != nil {
		return nil, err
	}
	w, err := openWAL(opt.Dir)
	if err != nil {
		return nil, err
	}
	s.wal = w
	s.wg.Add(1)
	go s.loop()
	return s, nil
}

func (s *Store) Dir() string { return s.opt.Dir }

func (s *Store) tier0() string { return filepath.Join(s.opt.Dir, "tier0") }

func (s *Store) seriesDir(id string) string {
	sum := sha1.Sum([]byte(id))
	h := hex.EncodeToString(sum[:])
	return filepath.Join(s.tier0(), h[:2], h)
}

// load rebuilds the block index by reading every block header.
func (s *Store) load(root string) error {
	n := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".blk") {
			return nil
		}
		id, meta, err := readHeader(path)
		if err != nil {
			s.log.Warn("tsdb: skipping corrupt block", "path", path, "err", err)
			return nil
		}
		sr := s.getOrCreate(id)
		sr.blocks = append(sr.blocks, meta)
		n++
		return nil
	})
	if err != nil {
		return err
	}
	for _, sr := range s.series {
		sort.Slice(sr.blocks, func(i, j int) bool { return sr.blocks[i].start < sr.blocks[j].start })
		for _, b := range sr.blocks {
			if b.end > sr.last {
				sr.last = b.end
			}
		}
	}
	s.log.Info("tsdb: loaded", "series", len(s.series), "blocks", n)
	return nil
}

func (s *Store) getOrCreate(id string) *series {
	s.mu.RLock()
	sr := s.series[id]
	s.mu.RUnlock()
	if sr != nil {
		return sr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sr = s.series[id]; sr != nil {
		return sr
	}
	sr = &series{id: id}
	s.series[id] = sr
	return sr
}

// tierFlush is a rollup buffer that filled while accepting one point.
// Nil on the common path so a quiet sample does not allocate it.
type tierFlush struct {
	t  *tier
	sr *tierSeries
}

// accept records one point. The caller holds checkpointMu for reading and
// does not hold wal.mu. A rejected timestamp returns ok false.
func (s *Store) accept(id string, ts int64, v float64) (sr *series, full bool, flushes []tierFlush, ok bool) {
	sr = s.getOrCreate(id)
	sr.mu.Lock()
	if ts <= sr.last {
		sr.mu.Unlock()
		return nil, false, nil, false
	}
	sr.last = ts
	sr.ts = append(sr.ts, ts)
	sr.vals = append(sr.vals, v)
	sr.dirty = true
	full = len(sr.ts) >= s.opt.BlockSize
	for _, t := range s.tiers {
		tsr := t.getOrCreate(id)
		if t.append(tsr, ts, v) {
			flushes = append(flushes, tierFlush{t, tsr})
		}
	}
	sr.mu.Unlock()
	return sr, full, flushes, true
}

func (s *Store) finishAppend(id string, sr *series, full bool, flushes []tierFlush) {
	// Archiving does not change logical data. A concurrent checkpoint can capture
	// either side; recovery deduplicates buffers against completed block files.
	if full {
		if err := s.flushSeries(sr); err != nil {
			s.log.Error("tsdb: flush failed", "series", id, "err", err)
		}
	}
	for _, p := range flushes {
		if err := p.t.flush(p.sr); err != nil {
			s.log.Error("tsdb: tier flush failed", "tier", p.t.idx, "series", id, "err", err)
		}
	}
}

// Append implements registry.Sink.
func (s *Store) Append(id string, ts int64, v float64) {
	s.checkpointMu.RLock()
	sr, full, flushes, ok := s.accept(id, ts, v)
	if !ok {
		s.checkpointMu.RUnlock()
		return
	}
	s.revision.Add(1)
	s.walWrite(id, ts, v)
	s.checkpointMu.RUnlock()
	s.finishAppend(id, sr, full, flushes)
}

// AppendMany writes one timestamp across several series under a single WAL
// lock. ids and vals are paired by index; a shorter slice ends the batch.
// A timestamp that is not newer than the series is skipped, matching Append.
func (s *Store) AppendMany(ts int64, ids []string, vals []float64) {
	n := len(ids)
	if len(vals) < n {
		n = len(vals)
	}
	if n == 0 {
		return
	}
	type accepted struct {
		id      string
		v       float64
		sr      *series
		full    bool
		flushes []tierFlush
	}
	s.checkpointMu.RLock()
	batch := make([]accepted, 0, n)
	for i := 0; i < n; i++ {
		sr, full, flushes, ok := s.accept(ids[i], ts, vals[i])
		if !ok {
			continue
		}
		batch = append(batch, accepted{ids[i], vals[i], sr, full, flushes})
	}
	if len(batch) == 0 {
		s.checkpointMu.RUnlock()
		return
	}
	s.revision.Add(uint64(len(batch)))
	if s.wal != nil {
		s.wal.mu.Lock()
		for _, p := range batch {
			if err := s.wal.appendLocked(p.id, ts, p.v); err != nil {
				s.log.Warn("tsdb: wal append failed", "series", p.id, "err", err)
			}
		}
		s.wal.mu.Unlock()
	}
	s.checkpointMu.RUnlock()
	for _, p := range batch {
		s.finishAppend(p.id, p.sr, p.full, p.flushes)
	}
}

// Series returns all known series IDs.
func (s *Store) Series() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.series))
	for id := range s.series {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Query returns raw points with after <= ts <= before, ascending.
func (s *Store) Query(id string, after, before int64) ([]Point, error) {
	if s.opt.Retention > 0 {
		after = max(after, time.Now().Add(-s.opt.Retention).Unix())
	}
	s.mu.RLock()
	sr := s.series[id]
	s.mu.RUnlock()
	if sr == nil {
		return nil, nil
	}
	sr.mu.Lock()
	blocks := make([]blockMeta, 0, len(sr.blocks))
	for _, b := range sr.blocks {
		if b.end >= after && b.start <= before {
			blocks = append(blocks, b)
		}
	}
	active := make([]Point, 0, len(sr.ts))
	for i, t := range sr.ts {
		if t >= after && t <= before {
			active = append(active, Point{t, sr.vals[i]})
		}
	}
	sr.mu.Unlock()

	out := make([]Point, 0, len(active)+len(blocks)*s.opt.BlockSize/4)
	for _, b := range blocks {
		ts, vals, err := readBlock(b.path)
		if err != nil {
			s.log.Warn("tsdb: unreadable block", "path", b.path, "err", err)
			continue
		}
		for i, t := range ts {
			if t >= after && t <= before {
				out = append(out, Point{t, vals[i]})
			}
		}
	}
	out = append(out, active...)
	return out, nil
}

// QueryAggregated folds one series into the same grid as AggregateBuckets.
// Tier 0 reads each raw block straight into the grid. Higher tiers decode
// only the rollup columns that group function needs, in the same block-then
// memory order as QueryTier.
func (s *Store) QueryAggregated(id string, tier int, after, before int64, points int, fn GroupFunc) (Result, error) {
	res := grid(after, before, points)
	f := newFold(res, fn)
	var err error
	if tier == 0 {
		err = s.foldRaw(id, after, before, f)
	} else {
		err = s.foldTier(id, tier, after, before, f)
	}
	if err != nil {
		return Result{}, err
	}
	res.Values = [][]float64{f.finish()}
	return res, nil
}

func (s *Store) foldTier(id string, tier int, after, before int64, f *fold) error {
	if tier < 1 || tier > len(s.tiers) {
		return fmt.Errorf("tier %d does not exist", tier)
	}
	t := s.tiers[tier-1]
	blocks, mem, lo, ok := t.snapshot(id, after, before)
	if !ok {
		return nil
	}
	every := t.spec.Every
	for _, b := range blocks {
		meta, data, err := readBlockData(b.path)
		if err != nil {
			continue
		}
		err = foldBucketBlock(data, meta.count, lo, before, every, f)
		putBuf(data)
		if err != nil {
			continue
		}
	}
	for _, b := range mem {
		f.addBucket(b, every)
	}
	return nil
}

func (s *Store) foldRaw(id string, after, before int64, f *fold) error {
	if s.opt.Retention > 0 {
		after = max(after, time.Now().Add(-s.opt.Retention).Unix())
	}
	s.mu.RLock()
	sr := s.series[id]
	s.mu.RUnlock()
	if sr == nil {
		return nil
	}
	sr.mu.Lock()
	blocks := make([]blockMeta, 0, len(sr.blocks))
	for _, b := range sr.blocks {
		if b.end >= after && b.start <= before {
			blocks = append(blocks, b)
		}
	}
	lo := sort.Search(len(sr.ts), func(i int) bool { return sr.ts[i] >= after })
	hi := sort.Search(len(sr.ts), func(i int) bool { return sr.ts[i] > before })
	if hi > len(sr.vals) {
		hi = len(sr.vals)
	}
	if lo > hi {
		lo = hi
	}
	activeTS := append([]int64(nil), sr.ts[lo:hi]...)
	activeVals := append([]float64(nil), sr.vals[lo:hi]...)
	sr.mu.Unlock()
	for _, b := range blocks {
		meta, data, err := readBlockData(b.path)
		if err != nil {
			s.log.Warn("tsdb: unreadable block", "path", b.path, "err", err)
			continue
		}
		f.save()
		err = decodeBlockEach(data, meta.count, func(t int64, v float64) {
			if t >= after && t <= before {
				f.addPoint(t, v)
			}
		})
		putBuf(data)
		if err != nil {
			f.restore()
			s.log.Warn("tsdb: unreadable block", "path", b.path, "err", err)
		}
	}
	for i, t := range activeTS {
		if t >= after && t <= before && i < len(activeVals) {
			f.addPoint(t, activeVals[i])
		}
	}
	return nil
}

// Bounds returns the oldest and newest timestamp stored for a series.
func (s *Store) Bounds(id string) (first, last int64, ok bool) {
	s.mu.RLock()
	sr := s.series[id]
	s.mu.RUnlock()
	if sr == nil {
		return 0, 0, false
	}
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if len(sr.blocks) > 0 {
		first, ok = sr.blocks[0].start, true
		last = sr.blocks[len(sr.blocks)-1].end
	}
	if len(sr.ts) > 0 {
		if !ok {
			first = sr.ts[0]
		}
		last, ok = sr.ts[len(sr.ts)-1], true
	}
	return
}

func (s *Store) flushSeries(sr *series) error {
	sr.flushMu.Lock()
	defer sr.flushMu.Unlock()
	sr.mu.Lock()
	if len(sr.ts) == 0 {
		sr.mu.Unlock()
		return nil
	}
	// Keep pending values queryable until the durable block is published.
	// Appends can extend the active buffer while this snapshot is being written.
	ts, vals := append([]int64(nil), sr.ts...), append([]float64(nil), sr.vals...)
	sr.mu.Unlock()
	if s.afterSnapshot != nil {
		s.afterSnapshot(sr)
	}
	meta, err := writeBlock(s.seriesDir(sr.id), sr.id, ts, vals)
	if err != nil {
		return err
	}
	sr.mu.Lock()
	sr.blocks = append(sr.blocks, meta)
	// A retention pass can drop a prefix while this block is written. Cutting
	// by snapshot length then deletes samples that arrived in that window, or
	// panics when the buffer is shorter. Those samples already advanced
	// sr.last, so later collections are rejected and last_entry stays put.
	sr.ts, sr.vals = suffixNewerThan(sr.ts, sr.vals, ts[len(ts)-1])
	sr.dirty = len(sr.ts) > 0
	if vis := sr.visibleEndLocked(); sr.last > vis {
		sr.last = vis
	}
	sr.mu.Unlock()
	return nil
}

// suffixNewerThan keeps samples strictly after end. ts is ordered ascending.
func suffixNewerThan(ts []int64, vals []float64, end int64) ([]int64, []float64) {
	i := sort.Search(len(ts), func(j int) bool { return ts[j] > end })
	if i > len(vals) {
		i = len(vals)
	}
	return ts[i:], vals[i:]
}

// visibleEndLocked is the newest timestamp still stored in a block or the
// active buffer. The caller holds sr.mu.
func (sr *series) visibleEndLocked() int64 {
	var end int64
	for _, b := range sr.blocks {
		if b.end > end {
			end = b.end
		}
	}
	if n := len(sr.ts); n > 0 && sr.ts[n-1] > end {
		end = sr.ts[n-1]
	}
	return end
}

// Flush persists one consistent image of all unfinished raw and rollup blocks.
// Sampling pauses only while the in-memory image is copied, not during I/O.
func (s *Store) Flush() (flushErr error) {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	startedAt := time.Now()
	started := startedAt.Unix()
	defer func() {
		s.persistenceMu.Lock()
		defer s.persistenceMu.Unlock()
		if flushErr != nil {
			s.persistence.Error = flushErr.Error()
			return
		}
		s.persistence.LastCheckpoint = started
		s.persistence.Finished = time.Now().Unix()
		s.persistence.DurationSeconds = time.Since(startedAt).Seconds()
		s.persistence.Error = ""
	}()
	s.checkpointMu.Lock()
	revision := s.revision.Load()
	if revision == s.flushedRevision {
		s.checkpointMu.Unlock()
		return nil
	}
	image := s.checkpointImage()
	walOff, walErr := s.walSyncSize()
	s.checkpointMu.Unlock()
	if walErr != nil {
		return walErr
	}
	if err := s.writeCheckpoint(image); err != nil {
		return err
	}
	s.checkpointMu.Lock()
	if err := s.walDiscard(walOff); err != nil {
		s.checkpointMu.Unlock()
		return err
	}
	s.checkpointMu.Unlock()
	s.flushedRevision = revision
	return nil
}

func (s *Store) loop() {
	defer s.wg.Done()
	gc := time.NewTicker(time.Minute)
	defer gc.Stop()
	walTick := time.NewTicker(time.Second)
	defer walTick.Stop()
	var cp <-chan time.Time
	if s.opt.Checkpoint > 0 {
		t := time.NewTicker(s.opt.Checkpoint)
		defer t.Stop()
		cp = t.C
	}
	for {
		select {
		case <-s.stop:
			return
		case <-walTick.C:
			if err := s.walSync(); err != nil {
				s.log.Warn("tsdb: wal sync failed", "err", err)
			}
		case <-gc.C:
			s.enforceRetention()
			for _, t := range s.tiers {
				t.enforceRetention(time.Now())
			}
			s.revision.Add(1)
		case <-cp:
			if err := s.Flush(); err != nil {
				s.log.Error("tsdb: checkpoint failed", "err", err)
			}
		}
	}
}

func (s *Store) enforceRetention() {
	s.checkpointMu.Lock()
	defer s.checkpointMu.Unlock()
	if s.opt.Retention <= 0 && s.opt.RetentionSize <= 0 {
		return
	}
	cutoff := int64(0)
	if s.opt.Retention > 0 {
		cutoff = time.Now().Add(-s.opt.Retention).Unix()
	}
	s.mu.RLock()
	all := make([]*series, 0, len(s.series))
	for _, sr := range s.series {
		all = append(all, sr)
	}
	s.mu.RUnlock()

	type ref struct {
		sr *series
		b  blockMeta
	}
	var total int64
	var refs []ref
	for _, sr := range all {
		sr.mu.Lock()
		if cutoff > 0 {
			n := sort.Search(len(sr.ts), func(i int) bool { return sr.ts[i] >= cutoff })
			if n > 0 {
				sr.ts = sr.ts[n:]
				sr.vals = sr.vals[n:]
				s.revision.Add(1)
			}
		}
		keep := sr.blocks[:0]
		for _, b := range sr.blocks {
			if cutoff > 0 && b.end < cutoff {
				_ = os.Remove(b.path)
				continue
			}
			keep = append(keep, b)
			total += b.size
			refs = append(refs, ref{sr, b})
		}
		sr.blocks = keep
		sr.mu.Unlock()
	}
	if s.opt.RetentionSize <= 0 || total <= s.opt.RetentionSize {
		return
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].b.start < refs[j].b.start })
	for _, r := range refs {
		if total <= s.opt.RetentionSize {
			break
		}
		_ = os.Remove(r.b.path)
		total -= r.b.size
		r.sr.mu.Lock()
		for i, b := range r.sr.blocks {
			if b.path == r.b.path {
				r.sr.blocks = append(r.sr.blocks[:i], r.sr.blocks[i+1:]...)
				break
			}
		}
		r.sr.mu.Unlock()
	}
}

// Close flushes active blocks and stops background work.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	close(s.stop)
	s.wg.Wait()
	err := s.Flush()
	if s.wal != nil {
		_ = s.wal.close()
	}
	return err
}

// ---- block file format ----
// magic(4) version(1) idLen(2) id start(8) end(8) count(4) dataLen(4) data

func writeBlock(dir, id string, ts []int64, vals []float64) (blockMeta, error) {
	return writeBlockData(dir, id, ts[0], ts[len(ts)-1], len(ts), encodeBlock(ts, vals))
}

// writeBlockData writes an already-encoded payload with the standard header.
func writeBlockData(dir, id string, start, end int64, count int, data []byte) (blockMeta, error) {
	return writeBlockFile(filepath.Join(dir, strconv.FormatInt(start, 10)+".blk"), id, start, end, count, data)
}

func writeBlockFile(path, id string, start, end int64, count int, data []byte) (blockMeta, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return blockMeta{}, err
	}
	var hdr bytes.Buffer
	hdr.WriteString(blockMagic)
	hdr.WriteByte(blockVersion)
	_ = binary.Write(&hdr, binary.LittleEndian, uint16(len(id)))
	hdr.WriteString(id)
	_ = binary.Write(&hdr, binary.LittleEndian, start)
	_ = binary.Write(&hdr, binary.LittleEndian, end)
	_ = binary.Write(&hdr, binary.LittleEndian, uint32(count))
	_ = binary.Write(&hdr, binary.LittleEndian, uint32(len(data)))
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return blockMeta{}, err
	}
	if _, err := f.Write(hdr.Bytes()); err != nil {
		f.Close()
		return blockMeta{}, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return blockMeta{}, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return blockMeta{}, err
	}
	if err := f.Close(); err != nil {
		return blockMeta{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return blockMeta{}, err
	}
	if err := syncBlockDir(filepath.Dir(path)); err != nil {
		return blockMeta{}, err
	}
	return blockMeta{path: path, start: start, end: end, count: count, size: int64(hdr.Len() + len(data))}, nil
}

func readHeader(path string) (string, blockMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", blockMeta{}, err
	}
	defer f.Close()
	id, meta, _, err := parseHeader(f)
	if err != nil {
		return "", blockMeta{}, err
	}
	meta.path = path
	st, err := f.Stat()
	if err == nil {
		meta.size = st.Size()
	}
	return id, meta, nil
}

func parseHeader(r io.Reader) (string, blockMeta, uint32, error) {
	// Batch the fixed prefix and variable-length remainder into two reads.
	// binary.Read per field otherwise performs a syscall for each small field
	// when the reader is a block file.
	var prefix [7]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return "", blockMeta{}, 0, err
	}
	if string(prefix[:4]) != blockMagic {
		return "", blockMeta{}, 0, errors.New("bad magic")
	}
	if prefix[4] != blockVersion {
		return "", blockMeta{}, 0, fmt.Errorf("unsupported block version %d", prefix[4])
	}
	idLen := int(binary.LittleEndian.Uint16(prefix[5:]))
	header := make([]byte, idLen+24)
	if _, err := io.ReadFull(r, header); err != nil {
		return "", blockMeta{}, 0, err
	}
	fields := header[idLen:]
	m := blockMeta{
		start: int64(binary.LittleEndian.Uint64(fields)),
		end:   int64(binary.LittleEndian.Uint64(fields[8:])),
	}
	count := binary.LittleEndian.Uint32(fields[16:])
	dataLen := binary.LittleEndian.Uint32(fields[20:])
	if count == 0 || count > maxBlockSamples || dataLen > maxBlockBytes || m.end < m.start {
		return "", blockMeta{}, 0, fmt.Errorf("implausible header: count=%d bytes=%d range=[%d,%d]", count, dataLen, m.start, m.end)
	}
	m.count = int(count)
	return string(header[:idLen]), m, dataLen, nil
}

func readBlock(path string) ([]int64, []float64, error) {
	meta, data, err := readBlockData(path)
	if err != nil {
		return nil, nil, err
	}
	defer putBuf(data)
	return decodeBlock(data, meta.count)
}

// blockBytes reuses file payloads across queries. Buffers larger than 1MiB
// are left for GC so one oversized block does not stay pinned.
var blockBytes sync.Pool

const pooledBlockBytes = 1 << 20

func getBuf(n int) []byte {
	if n == 0 {
		return []byte{}
	}
	if v := blockBytes.Get(); v != nil {
		b := v.([]byte)
		if cap(b) >= n {
			return b[:n]
		}
	}
	return make([]byte, n)
}

func putBuf(b []byte) {
	if cap(b) == 0 || cap(b) > pooledBlockBytes {
		return
	}
	blockBytes.Put(b[:0])
}

func readBlockData(path string) (blockMeta, []byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return blockMeta{}, nil, err
	}
	defer f.Close()
	_, meta, dataLen, err := parseHeader(f)
	if err != nil {
		return blockMeta{}, nil, err
	}
	data := getBuf(int(dataLen))
	if _, err := io.ReadFull(f, data); err != nil {
		putBuf(data)
		return blockMeta{}, nil, err
	}
	return meta, data, nil
}

// PersistenceStatus distinguishes a completed checkpoint from its configured cadence.
type PersistenceStatus struct {
	CheckpointBytes int64   `json:"checkpoint_bytes"`
	DurationSeconds float64 `json:"duration_seconds"`
	LastCheckpoint  int64   `json:"last_checkpoint"`
	Finished        int64   `json:"finished"`
	Error           string  `json:"error,omitempty"`
	IntervalSeconds float64 `json:"interval_seconds"`
}

func (s *Store) Persistence() PersistenceStatus {
	s.persistenceMu.Lock()
	defer s.persistenceMu.Unlock()
	out := s.persistence
	out.IntervalSeconds = s.opt.Checkpoint.Seconds()
	if stat, err := os.Stat(filepath.Join(s.opt.Dir, checkpointFile)); err == nil {
		out.CheckpointBytes = stat.Size()
	}
	return out
}
