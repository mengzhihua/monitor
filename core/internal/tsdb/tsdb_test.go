package tsdb

import (
	"encoding/binary"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGorillaRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	n := 5000
	ts := make([]int64, n)
	vals := make([]float64, n)
	base := time.Now().Unix()
	v := 42.0
	for i := 0; i < n; i++ {
		base += 1
		if i%97 == 0 {
			base += int64(r.Intn(5)) // occasional gaps
		}
		ts[i] = base
		switch {
		case i%13 == 0:
			v = float64(r.Intn(100))
		case i%7 == 0:
			// unchanged
		default:
			v += r.Float64()*2 - 1
		}
		vals[i] = v
	}
	vals[10] = 0
	vals[11] = -0.0
	vals[12] = math.MaxFloat64
	vals[13] = math.SmallestNonzeroFloat64
	buf := encodeBlock(ts, vals)
	gotTS, gotV, err := decodeBlock(buf, n)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotTS) != n {
		t.Fatalf("len %d != %d", len(gotTS), n)
	}
	for i := range ts {
		if gotTS[i] != ts[i] || math.Float64bits(gotV[i]) != math.Float64bits(vals[i]) {
			t.Fatalf("mismatch at %d: (%d,%v) != (%d,%v)", i, gotTS[i], gotV[i], ts[i], vals[i])
		}
	}
	bps := float64(len(buf)*8) / float64(n)
	t.Logf("compressed to %.2f bits/sample", bps)
	if bps > 64 {
		t.Errorf("no compression gain: %.2f bits/sample", bps)
	}
}

func TestGorillaCompressesTypicalMetrics(t *testing.T) {
	// per-second samples of a slowly changing integer-ish metric (e.g. CPU %)
	n := 3600
	ts := make([]int64, n)
	vals := make([]float64, n)
	for i := 0; i < n; i++ {
		ts[i] = 1_700_000_000 + int64(i)
		vals[i] = float64(20 + (i/30)%5)
	}
	buf := encodeBlock(ts, vals)
	bps := float64(len(buf)*8) / float64(n)
	t.Logf("typical metric: %.2f bits/sample", bps)
	if bps > 8 {
		t.Errorf("expected < 1 byte/sample, got %.2f bits", bps)
	}
}

func TestStoreFlushReloadQuery(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, BlockSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	start := int64(1_700_000_000)
	for i := int64(0); i < 250; i++ {
		s.Append("system.cpu|user", start+i, float64(i))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(Options{Dir: dir, BlockSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pts, err := s.Query("system.cpu|user", start+50, start+199)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 150 {
		t.Fatalf("got %d points, want 150", len(pts))
	}
	if pts[0].TS != start+50 || pts[0].Value != 50 || pts[149].TS != start+199 {
		t.Fatalf("bad bounds: %+v .. %+v", pts[0], pts[149])
	}
	first, last, ok := s.Bounds("system.cpu|user")
	if !ok || first != start || last != start+249 {
		t.Fatalf("bounds %d %d %v", first, last, ok)
	}
}

func TestAppendDropsSamplesOlderThanFlushedBlocks(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, BlockSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	start := int64(1_700_000_000)
	for i := int64(0); i < 10; i++ {
		s.Append("x", start+i, float64(i))
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	s.Append("x", start+5, 999) // replayed duplicate against an empty buffer
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(Options{Dir: dir, BlockSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Append("x", start+9, 999) // duplicate after restart
	s.Append("x", start+10, 10)
	pts, err := s.Query("x", start, start+20)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 11 {
		t.Fatalf("got %d points, want 11: %+v", len(pts), pts)
	}
	for i, p := range pts {
		if p.TS != start+int64(i) || p.Value != float64(i) {
			t.Fatalf("point %d = %+v", i, p)
		}
	}
}

func TestCorruptHeaderRejected(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, BlockSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(0); i < 10; i++ {
		s.Append("x", 1_700_000_000+i, 1)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var blk string
	_ = filepath.WalkDir(filepath.Join(dir, "tier0"), func(p string, d os.DirEntry, _ error) error {
		if !d.IsDir() && strings.HasSuffix(p, ".blk") {
			blk = p
		}
		return nil
	})
	if blk == "" {
		t.Fatal("no block written")
	}
	b, err := os.ReadFile(blk)
	if err != nil {
		t.Fatal(err)
	}
	// header: magic(4) ver(1) idLen(2) id(1) start(8) end(8) count(4) dataLen(4)
	off := 4 + 1 + 2 + 1 + 8 + 8
	binary.LittleEndian.PutUint32(b[off:], 0xFFFFFFFF)
	binary.LittleEndian.PutUint32(b[off+4:], 0xFFFFFFFF)
	if err := os.WriteFile(blk, b, 0o640); err != nil {
		t.Fatal(err)
	}
	s, err = Open(Options{Dir: dir, BlockSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, _, ok := s.Bounds("x"); ok {
		t.Fatal("corrupt block should have been skipped")
	}
}

func TestConcurrentFlushKeepsBlockOrder(t *testing.T) {
	s, err := Open(Options{Dir: t.TempDir(), BlockSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	start := int64(1_700_000_000)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			_ = s.Flush()
		}
	}()
	for i := int64(0); i < 2000; i++ {
		s.Append("x", start+i, float64(i))
	}
	<-done
	_ = s.Flush()
	sr := s.getOrCreate("x")
	sr.mu.Lock()
	defer sr.mu.Unlock()
	for i := 1; i < len(sr.blocks); i++ {
		if sr.blocks[i].start <= sr.blocks[i-1].end {
			t.Fatalf("blocks out of order at %d: %+v after %+v", i, sr.blocks[i], sr.blocks[i-1])
		}
	}
}

func TestAggregate(t *testing.T) {
	pts := []Point{{TS: 101, Value: 1}, {TS: 102, Value: 3}, {TS: 103, Value: 5}, {TS: 104, Value: 7}}
	res := Aggregate([][]Point{pts}, 100, 104, 2, GroupAverage)
	if res.Step != 2 || len(res.Times) != 2 || res.Times[0] != 102 || res.Times[1] != 104 {
		t.Fatalf("step=%d times=%v", res.Step, res.Times)
	}
	if res.Values[0][0] != 2 || res.Values[0][1] != 6 {
		t.Fatalf("values %v", res.Values[0])
	}
	res = Aggregate([][]Point{pts}, 100, 106, 3, GroupMax)
	if len(res.Times) != 3 || res.Values[0][1] != 7 || !math.IsNaN(res.Values[0][2]) {
		t.Fatalf("expected NaN for empty bucket, got %v (times %v)", res.Values[0], res.Times)
	}
}

func TestTiersRollupReloadAndPlan(t *testing.T) {
	dir := t.TempDir()
	tiers := []TierSpec{{Every: 60, BlockSize: 3}, {Every: 3600, BlockSize: 2}}
	s, err := Open(Options{Dir: dir, BlockSize: 500, Tiers: tiers})
	if err != nil {
		t.Fatal(err)
	}
	start := int64(1_700_000_000) - 1_700_000_000%3600 // hour aligned
	// 2.5 hours of 1s samples: value = second within the minute
	n := int64(9000)
	for i := int64(0); i < n; i++ {
		s.Append("a|x", start+i, float64(i%60))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(Options{Dir: dir, BlockSize: 500, Tiers: tiers})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	b1, err := s.QueryTier("a|x", 1, start, start+n-1)
	if err != nil {
		t.Fatal(err)
	}
	if len(b1) != 150 {
		t.Fatalf("tier1 buckets = %d, want 150", len(b1))
	}
	for i, b := range b1 {
		if b.TS != start+int64(i)*60 || b.Count != 60 || b.Min != 0 || b.Max != 59 || b.Sum != 1770 || b.Last != 59 {
			t.Fatalf("bucket %d = %+v", i, b)
		}
	}
	b2, err := s.QueryTier("a|x", 2, start, start+n-1)
	if err != nil {
		t.Fatal(err)
	}
	if len(b2) != 3 || b2[0].Count != 3600 || b2[2].Count != 1800 || b2[1].Avg() != 29.5 {
		t.Fatalf("tier2 = %+v", b2)
	}

	// planner: 1 day / 20 points → tier2; 10 min / 10 points → tier1; 1 min / 60 → tier0
	if tier := s.PlanTier(0, 86400, 20); tier != 2 {
		t.Fatalf("plan 1d/20 = %d", tier)
	}
	if tier := s.PlanTier(0, 600, 10); tier != 1 {
		t.Fatalf("plan 10m/10 = %d", tier)
	}
	if tier := s.PlanTier(0, 60, 60); tier != 0 {
		t.Fatalf("plan 1m/60 = %d", tier)
	}
	// auto: tier2 has data for the range → used; aggregation of average is exact
	bs, tier, err := s.QueryAuto("a|x", start, start+7200, 2)
	if err != nil || tier != 2 {
		t.Fatalf("auto tier = %d err %v", tier, err)
	}
	res := AggregateBuckets([][]Bucket{bs}, 3600, start, start+7200, 2, GroupAverage)
	if len(res.Values[0]) != 2 || res.Values[0][0] != 29.5 || res.Values[0][1] != 29.5 {
		t.Fatalf("avg = %v", res.Values[0])
	}
	res = AggregateBuckets([][]Bucket{b1[:2]}, 60, start, start+120, 1, GroupMax)
	if res.Values[0][0] != 59 {
		t.Fatalf("max = %v", res.Values[0])
	}
	// fallback: a series only present since a few seconds has no tier1 data yet
	s.Append("b|y", start+100_000, 1)
	bs, tier, err = s.QueryAuto("b|y", start+99_000, start+100_001, 5)
	if err != nil || tier != 1 || len(bs) != 1 || bs[0].Count != 1 {
		t.Fatalf("open bucket visible: tier=%d %+v %v", tier, bs, err)
	}
	infos := s.Tiers()
	if len(infos) != 3 || infos[1].Every != 60 || infos[1].Series != 2 || infos[1].Blocks == 0 || infos[2].Every != 3600 {
		t.Fatalf("tiers = %+v", infos)
	}
}

func TestTierRestartInsideBucketKeepsFolding(t *testing.T) {
	dir := t.TempDir()
	tiers := []TierSpec{{Every: 60, BlockSize: 4}}
	s, err := Open(Options{Dir: dir, Tiers: tiers})
	if err != nil {
		t.Fatal(err)
	}
	start := int64(1_700_000_000) - 1_700_000_000%60
	for i := int64(0); i < 90; i++ { // one full minute + 30s of the next
		s.Append("a", start+i, float64(i))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(Options{Dir: dir, Tiers: tiers})
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(90); i < 150; i++ { // rest of minute 2 + all of minute 3
		s.Append("a", start+i, float64(i))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(Options{Dir: dir, Tiers: tiers})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bs, err := s.QueryTier("a", 1, start, start+149)
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 3 {
		t.Fatalf("buckets = %+v", bs)
	}
	if b := bs[1]; b.Count != 60 || b.Min != 60 || b.Max != 119 || b.Last != 119 {
		t.Fatalf("bucket spanning the restart = %+v", b)
	}
	if bs[2].Count != 30 {
		t.Fatalf("open bucket after two restarts = %+v", bs[2])
	}
}

func TestTierCoversFallsBackToRawHistory(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, Tiers: []TierSpec{}}) // tier0 only, like an M0 database
	if err != nil {
		t.Fatal(err)
	}
	start := int64(1_700_000_000) - 1_700_000_000%3600
	for i := int64(0); i < 7200; i++ {
		s.Append("a", start+i, 1)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(Options{Dir: dir}) // upgrade: default tiers enabled
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := int64(7200); i < 7260; i++ { // a minute of new samples → one tier1/tier2 bucket each
		s.Append("a", start+i, 1)
	}
	if s.TierCovers("a", 2, start) || s.TierCovers("a", 1, start) {
		t.Fatal("tiers without the old history must not claim coverage")
	}
	if !s.TierCovers("a", 1, start+7200) {
		t.Fatal("tier1 covers the post-upgrade range")
	}
	bs, tier, err := s.QueryAuto("a", start, start+7260, 2)
	if err != nil || tier != 0 || len(bs) != 7260 {
		t.Fatalf("auto over upgrade boundary: tier=%d n=%d err=%v", tier, len(bs), err)
	}
	if _, tier, _ = s.QueryAuto("a", start+7200, start+7260, 1); tier != 1 {
		t.Fatalf("post-upgrade range should use tier1, got %d", tier)
	}
}

func TestBucketBlockRoundTrip(t *testing.T) {
	in := []Bucket{{TS: 60, Min: -1.5, Max: 9, Sum: 20.25, Last: 3, Count: 7}, {TS: 120, Min: 2, Max: 2, Sum: 2, Last: 2, Count: 1}}
	out, err := decodeBuckets(encodeBuckets(in), len(in))
	if err != nil {
		t.Fatal(err)
	}
	for i := range in {
		if in[i] != out[i] {
			t.Fatalf("bucket %d: %+v != %+v", i, in[i], out[i])
		}
	}
	if _, err := decodeBuckets(encodeBuckets(in)[:10], len(in)); err == nil {
		t.Fatal("truncated payload accepted")
	}
}

func TestTierRetentionDropsOldBlocks(t *testing.T) {
	s, err := Open(Options{Dir: t.TempDir(), Tiers: []TierSpec{{Every: 60, BlockSize: 2, Retention: time.Hour}}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old := time.Now().Add(-3 * time.Hour).Unix()
	for i := int64(0); i < 300; i++ { // 5 old minutes → 2 full blocks + 1 done/open
		s.Append("x", old+i, 1)
	}
	now := time.Now().Unix() - 120
	for i := int64(0); i < 180; i++ {
		s.Append("x", now+i, 1)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	s.tiers[0].enforceRetention(time.Now())
	bs, err := s.QueryTier("x", 1, 0, time.Now().Unix()+60)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bs {
		if b.TS < now-60 {
			t.Fatalf("old bucket survived: %+v", b)
		}
	}
	if len(bs) < 3 {
		t.Fatalf("recent buckets missing: %+v", bs)
	}
}
