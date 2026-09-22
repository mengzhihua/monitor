// Package tsdb is an embedded tiered time-series store. Tier 0 keeps
// full-resolution samples: an in-memory active block per series that is
// Gorilla-compressed into one file per block once it is full (or on
// checkpoint/close). Retention is enforced by deleting whole block files.
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
	checkpointMu  sync.RWMutex
	persistenceMu sync.Mutex
	persistence   PersistenceStatus
	opt           Options
	log           *slog.Logger
	mu            sync.RWMutex
	series        map[string]*series
	tiers         []*tier
	stop          chan struct{}
	wg            sync.WaitGroup
	closed        bool
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

// Append implements registry.Sink.
func (s *Store) Append(id string, ts int64, v float64) {
	sr := s.getOrCreate(id)
	sr.mu.Lock()
	if ts <= sr.last {
		sr.mu.Unlock()
		return // out of order / duplicate second: drop
	}
	sr.last = ts
	sr.ts = append(sr.ts, ts)
	sr.vals = append(sr.vals, v)
	sr.dirty = true
	full := len(sr.ts) >= s.opt.BlockSize
	sr.mu.Unlock()
	if full {
		if err := s.flushSeries(sr); err != nil {
			s.log.Error("tsdb: flush failed", "series", id, "err", err)
		}
	}
	for _, t := range s.tiers {
		tsr := t.getOrCreate(id)
		if t.append(tsr, ts, v) {
			if err := t.flush(tsr); err != nil {
				s.log.Error("tsdb: tier flush failed", "tier", t.idx, "series", id, "err", err)
			}
		}
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
	meta, err := writeBlock(s.seriesDir(sr.id), sr.id, ts, vals)
	if err != nil {
		return err
	}
	sr.mu.Lock()
	sr.blocks = append(sr.blocks, meta)
	sr.ts, sr.vals = sr.ts[len(ts):], sr.vals[len(vals):]
	sr.dirty = len(sr.ts) > 0
	sr.mu.Unlock()
	return nil
}

// Flush writes every active block to disk.
func (s *Store) Flush() (flushErr error) {
	s.checkpointMu.Lock()
	defer s.checkpointMu.Unlock()
	started := time.Now().Unix()
	defer func() {
		s.persistenceMu.Lock()
		defer s.persistenceMu.Unlock()
		if flushErr != nil {
			s.persistence.Error = flushErr.Error()
			return
		}
		s.persistence.LastCheckpoint = started
		s.persistence.Finished = time.Now().Unix()
		s.persistence.Error = ""
	}()
	s.mu.RLock()
	all := make([]*series, 0, len(s.series))
	for _, sr := range s.series {
		all = append(all, sr)
	}
	s.mu.RUnlock()
	var jobs []func() error
	for _, sr := range all {
		jobs = append(jobs, func() error { return s.flushSeries(sr) })
	}
	for _, t := range s.tiers {
		for _, sr := range t.all() {
			jobs = append(jobs, func() error {
				if err := t.flush(sr); err != nil {
					return err
				}
				return t.saveOpen(sr)
			})
		}
	}
	var firstErr error
	var errMu sync.Mutex
	var wg sync.WaitGroup
	work := make(chan func() error)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range work {
				if err := job(); err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errMu.Unlock()
				}
			}
		}()
	}
	for _, job := range jobs {
		work <- job
	}
	close(work)
	wg.Wait()
	return firstErr
}

func (s *Store) loop() {
	defer s.wg.Done()
	gc := time.NewTicker(time.Minute)
	defer gc.Stop()
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
		case <-gc.C:
			s.enforceRetention()
			for _, t := range s.tiers {
				t.enforceRetention(time.Now())
			}
		case <-cp:
			if err := s.Flush(); err != nil {
				s.log.Error("tsdb: checkpoint failed", "err", err)
			}
		}
	}
}

func (s *Store) enforceRetention() {
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
	return s.Flush()
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
	var magic [5]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil {
		return "", blockMeta{}, 0, err
	}
	if string(magic[:4]) != blockMagic {
		return "", blockMeta{}, 0, errors.New("bad magic")
	}
	if magic[4] != blockVersion {
		return "", blockMeta{}, 0, fmt.Errorf("unsupported block version %d", magic[4])
	}
	var idLen uint16
	if err := binary.Read(r, binary.LittleEndian, &idLen); err != nil {
		return "", blockMeta{}, 0, err
	}
	idb := make([]byte, idLen)
	if _, err := io.ReadFull(r, idb); err != nil {
		return "", blockMeta{}, 0, err
	}
	var m blockMeta
	var count, dataLen uint32
	if err := binary.Read(r, binary.LittleEndian, &m.start); err != nil {
		return "", blockMeta{}, 0, err
	}
	if err := binary.Read(r, binary.LittleEndian, &m.end); err != nil {
		return "", blockMeta{}, 0, err
	}
	if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
		return "", blockMeta{}, 0, err
	}
	if err := binary.Read(r, binary.LittleEndian, &dataLen); err != nil {
		return "", blockMeta{}, 0, err
	}
	if count == 0 || count > maxBlockSamples || dataLen > maxBlockBytes || m.end < m.start {
		return "", blockMeta{}, 0, fmt.Errorf("implausible header: count=%d bytes=%d range=[%d,%d]", count, dataLen, m.start, m.end)
	}
	m.count = int(count)
	return string(idb), m, dataLen, nil
}

func readBlock(path string) ([]int64, []float64, error) {
	meta, data, err := readBlockData(path)
	if err != nil {
		return nil, nil, err
	}
	return decodeBlock(data, meta.count)
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
	data := make([]byte, dataLen)
	if _, err := io.ReadFull(f, data); err != nil {
		return blockMeta{}, nil, err
	}
	return meta, data, nil
}

// PersistenceStatus distinguishes a completed checkpoint from its configured cadence.
type PersistenceStatus struct {
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
	return out
}
