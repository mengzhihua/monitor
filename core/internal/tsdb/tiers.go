package tsdb

import (
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// TierSpec describes one downsampled tier. Every sample appended to tier0 is
// folded into a bucket of Every seconds per tier; finished buckets are
// persisted in blocks of BlockSize buckets under <dir>/tier<N>/.
type TierSpec struct {
	Every     int64
	Retention time.Duration
	BlockSize int
}

// DefaultTiers mirrors Netdata's defaults: 1-minute and 1-hour rollups.
func DefaultTiers() []TierSpec {
	return []TierSpec{
		{Every: 60, Retention: 90 * 24 * time.Hour, BlockSize: 1440},
		{Every: 3600, Retention: 2 * 365 * 24 * time.Hour, BlockSize: 720},
	}
}

// Bucket is one downsampled interval [TS, TS+Every).
type Bucket struct {
	TS    int64   `json:"t"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	Sum   float64 `json:"sum"`
	Last  float64 `json:"last"`
	Count int64   `json:"n"`
}

// Avg returns the mean of the samples folded into the bucket.
func (b Bucket) Avg() float64 { return b.Sum / float64(b.Count) }

// TierInfo is the public description of a tier for /api/v1/info.
type TierInfo struct {
	Tier      int   `json:"tier"`
	Every     int64 `json:"update_every"`
	Retention int64 `json:"retention"` // seconds, 0 = unlimited
	Series    int   `json:"series"`
	Blocks    int   `json:"blocks"`
	Bytes     int64 `json:"bytes"`
	First     int64 `json:"first_time,omitempty"`
}

type tierSeries struct {
	id        string
	mu        sync.Mutex
	open      *Bucket
	savedOpen *Bucket
	done      []Bucket
	blocks    []blockMeta
	flushMu   sync.Mutex
}

type tier struct {
	idx    int
	spec   TierSpec
	dir    string
	mu     sync.RWMutex
	series map[string]*tierSeries
}

func newTier(idx int, spec TierSpec, root string) (*tier, error) {
	if spec.Every <= 0 {
		return nil, fmt.Errorf("tier%d: every must be > 0", idx)
	}
	if spec.BlockSize <= 0 {
		spec.BlockSize = 1024
	}
	dir := filepath.Join(root, fmt.Sprintf("tier%d", idx))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &tier{idx: idx, spec: spec, dir: dir, series: map[string]*tierSeries{}}, nil
}

func (t *tier) seriesDir(id string) string {
	sum := sha1.Sum([]byte(id))
	h := hex.EncodeToString(sum[:])
	return filepath.Join(t.dir, h[:2], h)
}

func (t *tier) getOrCreate(id string) *tierSeries {
	t.mu.RLock()
	sr := t.series[id]
	t.mu.RUnlock()
	if sr != nil {
		return sr
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if sr = t.series[id]; sr == nil {
		sr = &tierSeries{id: id}
		t.series[id] = sr
	}
	return sr
}

func (t *tier) get(id string) *tierSeries {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.series[id]
}

// openFile holds the bucket that was still accumulating when the store was
// closed, so a restart inside a minute/hour keeps folding into it instead of
// sealing a partial bucket.
const openFile = "open.bkt"

func (t *tier) load() (int, error) {
	n := 0
	var opens []string
	err := filepath.WalkDir(t.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() == openFile {
			opens = append(opens, path)
			return nil
		}
		if !strings.HasSuffix(path, ".blk") {
			return nil
		}
		id, meta, err := readHeader(path)
		if err != nil {
			return nil
		}
		sr := t.getOrCreate(id)
		sr.blocks = append(sr.blocks, meta)
		n++
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, sr := range t.series {
		sort.Slice(sr.blocks, func(i, j int) bool { return sr.blocks[i].start < sr.blocks[j].start })
	}
	for _, path := range opens {
		if id, b, ok := readOpen(path); ok {
			sr := t.getOrCreate(id)
			if last, has := sr.lastPersistedLocked(); !has || b.TS > last {
				sr.open = &b
			}
		}
	}
	return n, nil
}

func readOpen(path string) (string, Bucket, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", Bucket{}, false
	}
	defer f.Close()
	id, meta, dataLen, err := parseHeader(f)
	if err != nil || meta.count != 1 {
		return "", Bucket{}, false
	}
	data := make([]byte, dataLen)
	if _, err := io.ReadFull(f, data); err != nil {
		return "", Bucket{}, false
	}
	bs, err := decodeBuckets(data, 1)
	if err != nil {
		return "", Bucket{}, false
	}
	return id, bs[0], true
}

// saveOpen persists (or clears) the accumulating bucket of a series.
func (t *tier) saveOpen(sr *tierSeries) error {
	sr.mu.Lock()
	var b *Bucket
	if sr.open != nil {
		if sr.savedOpen != nil && *sr.savedOpen == *sr.open {
			sr.mu.Unlock()
			return nil
		}
		cp := *sr.open
		b = &cp
	}
	sr.mu.Unlock()
	path := filepath.Join(t.seriesDir(sr.id), openFile)
	if b == nil {
		err := os.Remove(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	_, err := writeBlockFile(path, sr.id, b.TS, b.TS, 1, encodeBuckets([]Bucket{*b}))
	if err == nil {
		sr.mu.Lock()
		sr.savedOpen = b
		sr.mu.Unlock()
	}
	return err
}

// append folds one raw sample; returns true when a block should be flushed.
func (t *tier) append(sr *tierSeries, ts int64, v float64) bool {
	start := ts - ts%t.spec.Every
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if sr.open != nil && sr.open.TS == start {
		b := sr.open
		if v < b.Min {
			b.Min = v
		}
		if v > b.Max {
			b.Max = v
		}
		b.Sum += v
		b.Last = v
		b.Count++
		return false
	}
	if sr.open != nil {
		if start < sr.open.TS {
			return false // out of order; tier0 already rejected it
		}
		sr.done = append(sr.done, *sr.open)
	} else if last, ok := sr.lastPersistedLocked(); ok && start <= last {
		return false // restarted inside a bucket that was already closed
	}
	sr.open = &Bucket{TS: start, Min: v, Max: v, Sum: v, Last: v, Count: 1}
	return len(sr.done) >= t.spec.BlockSize
}

func (sr *tierSeries) lastPersistedLocked() (int64, bool) {
	if n := len(sr.done); n > 0 {
		return sr.done[n-1].TS, true
	}
	if n := len(sr.blocks); n > 0 {
		return sr.blocks[n-1].end, true
	}
	return 0, false
}

func (t *tier) flush(sr *tierSeries) error {
	sr.flushMu.Lock()
	defer sr.flushMu.Unlock()
	sr.mu.Lock()
	if len(sr.done) == 0 {
		sr.mu.Unlock()
		return nil
	}
	done := append([]Bucket(nil), sr.done...)
	sr.mu.Unlock()

	meta, err := writeBlockData(t.seriesDir(sr.id), sr.id, done[0].TS, done[len(done)-1].TS, len(done), encodeBuckets(done))
	if err != nil {
		return err
	}
	sr.mu.Lock()
	sr.blocks = append(sr.blocks, meta)
	sr.done = sr.done[len(done):]
	sr.mu.Unlock()
	return nil
}

func (t *tier) all() []*tierSeries {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]*tierSeries, 0, len(t.series))
	for _, sr := range t.series {
		out = append(out, sr)
	}
	return out
}

func (t *tier) flushAll() error {
	var firstErr error
	for _, sr := range t.all() {
		if err := t.flush(sr); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (t *tier) enforceRetention(now time.Time) {
	if t.spec.Retention <= 0 {
		return
	}
	cutoff := now.Add(-t.spec.Retention).Unix()
	for _, sr := range t.all() {
		sr.mu.Lock()
		n := sort.Search(len(sr.done), func(i int) bool { return sr.done[i].TS+t.spec.Every > cutoff })
		sr.done = sr.done[n:]
		if sr.open != nil && sr.open.TS+t.spec.Every <= cutoff {
			sr.open = nil
		}
		keep := sr.blocks[:0]
		for _, b := range sr.blocks {
			if b.end+t.spec.Every <= cutoff {
				_ = os.Remove(b.path)
				continue
			}
			keep = append(keep, b)
		}
		sr.blocks = keep
		sr.mu.Unlock()
	}
}

func (t *tier) query(id string, after, before int64) ([]Bucket, error) {
	var out []Bucket
	err := t.walk(id, after, before, [5]bool{true, true, true, true, true}, func(b Bucket) {
		out = append(out, b)
	})
	return out, err
}

// snapshot copies the blocks and in-memory buckets visible for [after, before].
// lo is the oldest bucket start that can still cover after.
func (t *tier) snapshot(id string, after, before int64) (blocks []blockMeta, mem []Bucket, lo int64, ok bool) {
	sr := t.get(id)
	if sr == nil {
		return nil, nil, 0, false
	}
	// a bucket starting at TS covers up to TS+Every-1
	lo = after - t.spec.Every + 1
	// Retention is enforced per block on disk; a block can straddle the cutoff,
	// so also hide expired buckets at read time.
	if t.spec.Retention > 0 {
		if cut := time.Now().Add(-t.spec.Retention).Unix() - t.spec.Every + 1; cut > lo {
			lo = cut
		}
	}
	sr.mu.Lock()
	blocks = make([]blockMeta, 0, len(sr.blocks))
	for _, b := range sr.blocks {
		if b.end >= lo && b.start <= before {
			blocks = append(blocks, b)
		}
	}
	mem = make([]Bucket, 0, len(sr.done)+1)
	for _, b := range sr.done {
		if b.TS >= lo && b.TS <= before {
			mem = append(mem, b)
		}
	}
	if sr.open != nil && sr.open.TS >= lo && sr.open.TS <= before {
		mem = append(mem, *sr.open)
	}
	sr.mu.Unlock()
	return blocks, mem, lo, true
}

// walk emits buckets in block-then-memory order. cols selects which gorilla
// columns to decode (min, max, sum, last, count); memory buckets are already
// complete and ignore cols.
func (t *tier) walk(id string, after, before int64, cols [5]bool, emit func(Bucket)) error {
	blocks, mem, lo, ok := t.snapshot(id, after, before)
	if !ok {
		return nil
	}
	for _, b := range blocks {
		meta, data, err := readBlockData(b.path)
		if err != nil {
			continue
		}
		err = decodeBucketsEach(data, meta.count, cols, func(x Bucket) {
			if x.TS >= lo && x.TS <= before {
				emit(x)
			}
		})
		putBuf(data)
		if err != nil {
			continue
		}
	}
	for _, b := range mem {
		emit(b)
	}
	return nil
}

// foldBucketBlock adds one on-disk rollup block straight into the grid.
// A truncated column restores the fold to its state before this block.
func foldBucketBlock(buf []byte, n int, lo, before, every int64, f *fold) error {
	chunks, err := splitBucketCols(buf)
	if err != nil {
		return err
	}
	if f.fn == GroupMedian {
		return decodeBucketsEach(buf, n, colsForGroup(GroupMedian), func(b Bucket) {
			if b.TS >= lo && b.TS <= before {
				f.addBucket(b, every)
			}
		})
	}
	cols := colsForGroup(f.fn)
	f.save()
	for col := 0; col < 5; col++ {
		if !cols[col] {
			continue
		}
		err := decodeBlockEach(chunks[col], n, func(ts int64, v float64) {
			if ts < lo || ts > before {
				return
			}
			b := Bucket{TS: ts}
			switch col {
			case 0:
				b.Min = v
			case 1:
				b.Max = v
			case 2:
				b.Sum = v
			case 3:
				b.Last = v
			case 4:
				b.Count = int64(v)
			}
			f.addBucket(b, every)
		})
		if err != nil {
			f.restore()
			return err
		}
	}
	return nil
}

// colsForGroup is the rollup columns a reducer reads. Average and median
// need sum and count; the others need a single column.
func colsForGroup(fn GroupFunc) [5]bool {
	switch fn {
	case GroupMin:
		return [5]bool{true, false, false, false, false}
	case GroupMax:
		return [5]bool{false, true, false, false, false}
	case GroupSum:
		return [5]bool{false, false, true, false, false}
	case GroupLast:
		return [5]bool{false, false, false, true, false}
	default:
		return [5]bool{false, false, true, false, true}
	}
}

// first returns the start of the oldest bucket held for a series.
func (t *tier) first(id string) (int64, bool) {
	sr := t.get(id)
	if sr == nil {
		return 0, false
	}
	sr.mu.Lock()
	defer sr.mu.Unlock()
	switch {
	case len(sr.blocks) > 0:
		return sr.blocks[0].start, true
	case len(sr.done) > 0:
		return sr.done[0].TS, true
	case sr.open != nil:
		return sr.open.TS, true
	}
	return 0, false
}

func (t *tier) info() TierInfo {
	inf := TierInfo{Tier: t.idx, Every: t.spec.Every, Retention: int64(t.spec.Retention / time.Second)}
	for _, sr := range t.all() {
		sr.mu.Lock()
		if len(sr.blocks) > 0 || len(sr.done) > 0 || sr.open != nil {
			inf.Series++
		}
		for _, b := range sr.blocks {
			inf.Blocks++
			inf.Bytes += b.size
			if inf.First == 0 || b.start < inf.First {
				inf.First = b.start
			}
		}
		if len(sr.blocks) == 0 && len(sr.done) > 0 && (inf.First == 0 || sr.done[0].TS < inf.First) {
			inf.First = sr.done[0].TS
		}
		sr.mu.Unlock()
	}
	return inf
}

// ---- bucket block payload ----
// Five gorilla sub-blocks sharing the timestamp stream: min max sum last count,
// each prefixed with its byte length.

func encodeBuckets(bs []Bucket) []byte {
	ts := make([]int64, len(bs))
	cols := make([][]float64, 5)
	for i := range cols {
		cols[i] = make([]float64, len(bs))
	}
	for i, b := range bs {
		ts[i] = b.TS
		cols[0][i], cols[1][i], cols[2][i], cols[3][i], cols[4][i] = b.Min, b.Max, b.Sum, b.Last, float64(b.Count)
	}
	var out []byte
	for _, c := range cols {
		enc := encodeBlock(ts, c)
		out = binary.LittleEndian.AppendUint32(out, uint32(len(enc)))
		out = append(out, enc...)
	}
	return out
}

func decodeBuckets(buf []byte, n int) ([]Bucket, error) {
	bs := make([]Bucket, 0, n)
	err := decodeBucketsEach(buf, n, [5]bool{true, true, true, true, true}, func(b Bucket) {
		bs = append(bs, b)
	})
	if err != nil {
		return nil, err
	}
	return bs, nil
}

func splitBucketCols(buf []byte) ([5][]byte, error) {
	var chunks [5][]byte
	for col := 0; col < 5; col++ {
		if len(buf) < 4 {
			return chunks, errors.New("truncated bucket block")
		}
		l := int(binary.LittleEndian.Uint32(buf))
		buf = buf[4:]
		if l > len(buf) {
			return chunks, errors.New("truncated bucket block")
		}
		chunks[col] = buf[:l]
		buf = buf[l:]
	}
	return chunks, nil
}

// decodeBucketsEach decodes a bucket block and calls emit once per sample.
// cols selects gorilla columns (min, max, sum, last, count). Unselected
// columns are skipped. Timestamps come from the first decoded column; every
// column was encoded from the same timestamp stream. emit runs only after
// every selected column decodes, so a truncated block adds nothing.
func decodeBucketsEach(buf []byte, n int, cols [5]bool, emit func(Bucket)) error {
	chunks, err := splitBucketCols(buf)
	if err != nil {
		return err
	}
	var ts []int64
	var colv [5][]float64
	for col := 0; col < 5; col++ {
		if !cols[col] {
			continue
		}
		cts, vals, err := decodeBlock(chunks[col], n)
		if err != nil {
			return err
		}
		if ts == nil {
			ts = cts
		}
		colv[col] = vals
	}
	for i := 0; i < n && i < len(ts); i++ {
		b := Bucket{TS: ts[i]}
		if v := colv[0]; i < len(v) {
			b.Min = v[i]
		}
		if v := colv[1]; i < len(v) {
			b.Max = v[i]
		}
		if v := colv[2]; i < len(v) {
			b.Sum = v[i]
		}
		if v := colv[3]; i < len(v) {
			b.Last = v[i]
		}
		if v := colv[4]; i < len(v) {
			b.Count = int64(v[i])
		}
		emit(b)
	}
	return nil
}

// ---- Store integration ----

// Tiers describes tier0 plus every configured rollup tier.
func (s *Store) Tiers() []TierInfo {
	t0 := TierInfo{Tier: 0, Every: 1, Retention: int64(s.opt.Retention / time.Second)}
	s.mu.RLock()
	all := make([]*series, 0, len(s.series))
	for _, sr := range s.series {
		all = append(all, sr)
	}
	s.mu.RUnlock()
	for _, sr := range all {
		sr.mu.Lock()
		t0.Series++
		for _, b := range sr.blocks {
			t0.Blocks++
			t0.Bytes += b.size
			if t0.First == 0 || b.start < t0.First {
				t0.First = b.start
			}
		}
		if len(sr.blocks) == 0 && len(sr.ts) > 0 && (t0.First == 0 || sr.ts[0] < t0.First) {
			t0.First = sr.ts[0]
		}
		sr.mu.Unlock()
	}
	out := []TierInfo{t0}
	for _, t := range s.tiers {
		out = append(out, t.info())
	}
	return out
}

// TierEvery returns the bucket width of a tier (1 for tier0), or false when
// the tier does not exist.
func (s *Store) TierEvery(tier int) (int64, bool) {
	if tier == 0 {
		return 1, true
	}
	if tier < 0 || tier > len(s.tiers) {
		return 0, false
	}
	return s.tiers[tier-1].spec.Every, true
}

// PlanTier picks the coarsest tier whose bucket width still gives at least
// one bucket per requested output point.
func (s *Store) PlanTier(after, before int64, points int) int {
	if points <= 0 || before <= after {
		return 0
	}
	step := (before - after) / int64(points)
	best := 0
	for i, t := range s.tiers {
		if t.spec.Every <= step {
			best = i + 1
		}
	}
	return best
}

// QueryTier returns the buckets of one series in [after, before] from the
// given tier. Tier0 raw points are wrapped as single-sample buckets.
func (s *Store) QueryTier(id string, tier int, after, before int64) ([]Bucket, error) {
	if tier == 0 {
		pts, err := s.Query(id, after, before)
		if err != nil {
			return nil, err
		}
		out := make([]Bucket, len(pts))
		for i, p := range pts {
			out[i] = Bucket{TS: p.TS, Min: p.Value, Max: p.Value, Sum: p.Value, Last: p.Value, Count: 1}
		}
		return out, nil
	}
	if tier < 0 || tier > len(s.tiers) {
		return nil, fmt.Errorf("tier %d does not exist", tier)
	}
	return s.tiers[tier-1].query(id, after, before)
}

// TierCovers reports whether a tier reaches as far back into [after, ...] as
// the raw data does. A tier that was enabled after tier0 already held
// history (or one that is still empty) has no rollups for the older
// samples, so answering from it would truncate the range rather than just
// lower its resolution.
func (s *Store) TierCovers(id string, tier int, after int64) bool {
	if tier <= 0 || tier > len(s.tiers) {
		return true
	}
	rawFirst, _, ok := s.Bounds(id)
	if !ok {
		return true
	}
	want := max(rawFirst, after)
	first, ok := s.tiers[tier-1].first(id)
	return ok && first <= want
}

// QueryAuto plans a tier for the request and falls back to finer tiers while
// the planned one does not cover the range (fresh install, tier added after
// the fact). It returns the buckets and the tier actually used.
func (s *Store) QueryAuto(id string, after, before int64, points int) ([]Bucket, int, error) {
	tier := s.PlanTier(after, before, points)
	for tier > 0 && !s.TierCovers(id, tier, after) {
		tier--
	}
	bs, err := s.QueryTier(id, tier, after, before)
	if err != nil {
		return nil, 0, err
	}
	return bs, tier, nil
}

// AggregateBuckets is Aggregate over pre-rolled buckets. min/max/sum are
// exact; average is sample-weighted; median falls back to bucket means.
func AggregateBuckets(series [][]Bucket, every int64, after, before int64, points int, fn GroupFunc) Result {
	res := grid(after, before, points)
	res.Values = make([][]float64, len(series))
	f := newFold(res, fn)
	for si, bs := range series {
		if si > 0 {
			f.reset(fn)
		}
		for _, b := range bs {
			f.addBucket(b, every)
		}
		res.Values[si] = f.finish()
	}
	return res
}

func reduceBuckets(g []Bucket, fn GroupFunc) float64 {
	switch fn {
	case GroupMin:
		m := g[0].Min
		for _, b := range g[1:] {
			if b.Min < m {
				m = b.Min
			}
		}
		return m
	case GroupMax:
		m := g[0].Max
		for _, b := range g[1:] {
			if b.Max > m {
				m = b.Max
			}
		}
		return m
	case GroupSum:
		var s float64
		for _, b := range g {
			s += b.Sum
		}
		return s
	case GroupMedian:
		v := make([]float64, len(g))
		for i, b := range g {
			v[i] = b.Avg()
		}
		return reduce(v, GroupMedian)
	case GroupLast:
		return g[len(g)-1].Last
	default:
		var s float64
		var c int64
		for _, b := range g {
			s += b.Sum
			c += b.Count
		}
		if c == 0 {
			return math.NaN()
		}
		return s / float64(c)
	}
}
