package tsdb

import (
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
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
	id      string
	mu      sync.Mutex
	open    *Bucket
	done    []Bucket
	blocks  []blockMeta
	flushMu sync.Mutex
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

func (t *tier) load() (int, error) {
	n := 0
	err := filepath.WalkDir(t.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".blk") {
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
	return n, nil
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

// seal moves the open bucket into done so it is persisted by the next flush.
func (t *tier) seal(sr *tierSeries) {
	sr.mu.Lock()
	if sr.open != nil {
		sr.done = append(sr.done, *sr.open)
		sr.open = nil
	}
	sr.mu.Unlock()
}

func (t *tier) flush(sr *tierSeries) error {
	sr.flushMu.Lock()
	defer sr.flushMu.Unlock()
	sr.mu.Lock()
	if len(sr.done) == 0 {
		sr.mu.Unlock()
		return nil
	}
	done := sr.done
	sr.done = nil
	sr.mu.Unlock()

	meta, err := writeBlockData(t.seriesDir(sr.id), sr.id, done[0].TS, done[len(done)-1].TS, len(done), encodeBuckets(done))
	if err != nil {
		sr.mu.Lock()
		sr.done = append(done, sr.done...)
		sr.mu.Unlock()
		return err
	}
	sr.mu.Lock()
	sr.blocks = append(sr.blocks, meta)
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
	sr := t.get(id)
	if sr == nil {
		return nil, nil
	}
	// a bucket starting at TS covers up to TS+Every-1
	lo := after - t.spec.Every + 1
	sr.mu.Lock()
	blocks := make([]blockMeta, 0, len(sr.blocks))
	for _, b := range sr.blocks {
		if b.end >= lo && b.start <= before {
			blocks = append(blocks, b)
		}
	}
	mem := make([]Bucket, 0, len(sr.done)+1)
	for _, b := range sr.done {
		if b.TS >= lo && b.TS <= before {
			mem = append(mem, b)
		}
	}
	if sr.open != nil && sr.open.TS >= lo && sr.open.TS <= before {
		mem = append(mem, *sr.open)
	}
	sr.mu.Unlock()

	out := make([]Bucket, 0, len(mem)+len(blocks)*t.spec.BlockSize/4)
	for _, b := range blocks {
		meta, data, err := readBlockData(b.path)
		if err != nil {
			continue
		}
		bs, err := decodeBuckets(data, meta.count)
		if err != nil {
			continue
		}
		for _, x := range bs {
			if x.TS >= lo && x.TS <= before {
				out = append(out, x)
			}
		}
	}
	return append(out, mem...), nil
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
	bs := make([]Bucket, n)
	for col := 0; col < 5; col++ {
		if len(buf) < 4 {
			return nil, errors.New("truncated bucket block")
		}
		l := int(binary.LittleEndian.Uint32(buf))
		buf = buf[4:]
		if l > len(buf) {
			return nil, errors.New("truncated bucket block")
		}
		ts, vals, err := decodeBlock(buf[:l], n)
		if err != nil {
			return nil, err
		}
		buf = buf[l:]
		for i := range bs {
			switch col {
			case 0:
				bs[i].TS, bs[i].Min = ts[i], vals[i]
			case 1:
				bs[i].Max = vals[i]
			case 2:
				bs[i].Sum = vals[i]
			case 3:
				bs[i].Last = vals[i]
			case 4:
				bs[i].Count = int64(vals[i])
			}
		}
	}
	return bs, nil
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

// QueryAuto plans a tier for the request and falls back to finer tiers when
// the planned one holds nothing for the range (e.g. a fresh install). It
// returns the buckets and the tier actually used.
func (s *Store) QueryAuto(id string, after, before int64, points int) ([]Bucket, int, error) {
	for tier := s.PlanTier(after, before, points); ; tier-- {
		bs, err := s.QueryTier(id, tier, after, before)
		if err != nil {
			return nil, 0, err
		}
		if len(bs) > 0 || tier == 0 {
			return bs, tier, nil
		}
	}
}

// AggregateBuckets is Aggregate over pre-rolled buckets. min/max/sum are
// exact; average is sample-weighted; median falls back to bucket means.
func AggregateBuckets(series [][]Bucket, every int64, after, before int64, points int, fn GroupFunc) Result {
	if before <= after {
		before = after + 1
	}
	span := before - after
	if points <= 0 {
		points = int(span)
	}
	step := (span + int64(points) - 1) / int64(points)
	if step < 1 {
		step = 1
	}
	after = after - (after % step)
	n := int((before - after + step - 1) / step)
	times := make([]int64, n)
	for i := range times {
		times[i] = after + int64(i+1)*step
	}
	if every < 1 {
		every = 1
	}
	res := Result{After: after, Before: before, Step: step, Times: times, Values: make([][]float64, len(series))}
	for si, bs := range series {
		out := make([]float64, n)
		for i := range out {
			out[i] = math.NaN()
		}
		groups := make([][]Bucket, n)
		for _, b := range bs {
			key := b.TS + every - 1 // last instant the bucket may cover
			if key > before && b.TS <= before {
				key = before // bucket still filling at the end of the range
			}
			if key <= after || key > times[n-1] {
				continue
			}
			bi := int((key - after - 1) / step)
			if bi < 0 || bi >= n {
				continue
			}
			groups[bi] = append(groups[bi], b)
		}
		for i, g := range groups {
			if len(g) == 0 {
				continue
			}
			out[i] = reduceBuckets(g, fn)
		}
		res.Values[si] = out
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
