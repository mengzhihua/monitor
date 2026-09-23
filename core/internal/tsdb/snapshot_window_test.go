package tsdb

import (
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"
)

// Keep the former full-capacity scan as an independent selection oracle.
func fullScanBlocks(blocks []blockMeta, after, before int64) []blockMeta {
	out := make([]blockMeta, 0, len(blocks))
	for _, b := range blocks {
		if b.end >= after && b.start <= before {
			out = append(out, b)
		}
	}
	return out
}

func TestSnapshotBlocksWindowsAndOverlaps(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	blocks := []blockMeta{{start: 0, end: 10000}, {start: 0, end: 0}}
	for i := 0; i < 1000; i++ {
		start := rng.Int63n(2000)
		blocks = append(blocks, blockMeta{start: start, end: start + rng.Int63n(200)})
	}
	sort.SliceStable(blocks, func(i, j int) bool { return blocks[i].start < blocks[j].start })
	original := append([]blockMeta(nil), blocks...)
	windows := [][2]int64{{-20, -1}, {0, 0}, {1, 1}, {500, 501}, {500, 1500}, {-1, 20000}, {20000, 30000}, {100, 50}}
	for i := 0; i < 100; i++ {
		windows = append(windows, [2]int64{rng.Int63n(2500), rng.Int63n(2500)})
	}
	for _, window := range windows {
		for _, source := range [][]blockMeta{nil, blocks} {
			got, want := snapshotBlocks(source, window[0], window[1]), fullScanBlocks(source, window[0], window[1])
			if len(got) != len(want) {
				t.Fatalf("window=%v count=%d want=%d", window, len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("window=%v row=%d differs", window, i)
				}
			}
			if cap(got) != len(got) {
				t.Fatalf("unrelated blocks reserved: len=%d cap=%d", len(got), cap(got))
			}
			if len(got) > 0 {
				got[0].end = -100
			}
		}
	}
	if !reflect.DeepEqual(blocks, original) {
		t.Fatal("snapshot aliases block index")
	}
}

func TestTierSnapshotWindowAndIsolation(t *testing.T) {
	sr := &tierSeries{blocks: []blockMeta{{start: 0, end: 9}, {start: 10, end: 19}},
		done: []Bucket{{TS: 20, Count: 1}, {TS: 30, Count: 2}, {TS: 40, Count: 3}}, open: &Bucket{TS: 50, Count: 4}}
	tier := &tier{spec: TierSpec{Every: 10}, series: map[string]*tierSeries{"metric": sr}}
	for _, window := range [][2]int64{{-20, -10}, {20, 20}, {29, 30}, {39, 50}, {59, 59}, {60, 70}, {0, 100}, {60, 10}} {
		blocks, mem, lo, ok := tier.snapshot("metric", window[0], window[1])
		if !ok || lo != window[0]-9 {
			t.Fatalf("window=%v bounds", window)
		}
		wantBlocks := fullScanBlocks(sr.blocks, lo, window[1])
		if len(blocks) != len(wantBlocks) {
			t.Fatalf("window=%v blocks", window)
		}
		for i := range blocks {
			if blocks[i] != wantBlocks[i] {
				t.Fatalf("window=%v block %d", window, i)
			}
		}
		var want []Bucket
		for _, b := range append(append([]Bucket(nil), sr.done...), *sr.open) {
			if b.TS >= lo && b.TS <= window[1] {
				want = append(want, b)
			}
		}
		if len(mem) != len(want) {
			t.Fatalf("window=%v buckets=%v want=%v", window, mem, want)
		}
		for i := range want {
			if mem[i] != want[i] {
				t.Fatalf("window=%v bucket %d", window, i)
			}
		}
		if cap(mem) != len(mem) {
			t.Fatalf("window=%v reserved unrelated buckets", window)
		}
		if len(mem) > 0 {
			mem[0].Count = 999
		}
	}
	if sr.done[0].Count != 1 || sr.done[1].Count != 2 || sr.open.Count != 4 {
		t.Fatal("snapshot aliases buckets")
	}
	if _, _, _, ok := tier.snapshot("missing", 0, 100); ok {
		t.Fatal("missing series exists")
	}
}

func TestQueryWindowsAcrossRestart(t *testing.T) {
	opt := Options{Dir: t.TempDir(), BlockSize: 4, Tiers: []TierSpec{{Every: 10, BlockSize: 3}}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			_ = s.Close()
		}
	}()
	var points []Point
	for i := int64(0); i < 27; i++ {
		p := Point{TS: 100 + i*3, Value: float64(i%7 + 1)}
		points = append(points, p)
		s.Append("fixture", p.TS, p.Value)
	}
	check := func() {
		t.Helper()
		for _, window := range [][2]int64{{0, 99}, {100, 100}, {111, 125}, {160, 173}, {172, 181}, {179, 200}, {0, 200}, {180, 160}} {
			var want []Point
			for _, p := range points {
				if p.TS >= window[0] && p.TS <= window[1] {
					want = append(want, p)
				}
			}
			got, err := s.Query("fixture", window[0], window[1])
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(want) {
				t.Fatalf("window=%v raw=%v want=%v", window, got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("raw window=%v differs", window)
				}
			}
			if window[0] >= window[1] {
				continue
			}
			for _, tier := range []int{0, 1} {
				buckets, err := s.QueryTier("fixture", tier, window[0], window[1])
				if err != nil {
					t.Fatal(err)
				}
				for _, fn := range []GroupFunc{GroupAverage, GroupSum, GroupMin, GroupMax, GroupLast, GroupMedian} {
					result, err := s.QueryAggregated("fixture", tier, window[0], window[1], 7, fn)
					if err != nil {
						t.Fatal(err)
					}
					var expected Result
					if tier == 0 {
						expected = Aggregate([][]Point{want}, window[0], window[1], 7, fn)
					} else {
						expected = AggregateBuckets([][]Bucket{buckets}, 10, window[0], window[1], 7, fn)
					}
					if !reflect.DeepEqual(result.Times, expected.Times) {
						t.Fatalf("window=%v grid", window)
					}
					for i, v := range expected.Values[0] {
						g := result.Values[0][i]
						if g != v && !(math.IsNaN(g) && math.IsNaN(v)) {
							t.Fatalf("tier=%d window=%v fn=%s bucket=%d got=%v want=%v", tier, window, fn, i, g, v)
						}
					}
				}
			}
		}
	}
	check()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	check()
}

func TestTierSnapshotWindowRetention(t *testing.T) {
	now := time.Now().Unix()
	sr := &tierSeries{
		blocks: []blockMeta{{start: now - 200, end: now - 150}, {start: now - 55, end: now - 50}},
		done:   []Bucket{{TS: now - 100}, {TS: now - 50}, {TS: now - 10}},
		open:   &Bucket{TS: now},
	}
	tier := &tier{spec: TierSpec{Every: 10, Retention: time.Minute}, series: map[string]*tierSeries{"metric": sr}}
	blocks, mem, lo, ok := tier.snapshot("metric", 0, now+1)
	if !ok || lo < now-69 || len(blocks) != 1 || blocks[0].start != now-55 || len(mem) != 3 || mem[0].TS != now-50 || mem[2].TS != now {
		t.Fatalf("retention snapshot: lo=%d blocks=%v mem=%v", lo, blocks, mem)
	}
}

// Match the old snapshot path for fixed fixtures without retention. The real
// selection test above independently checks window boundaries and ownership.
func fullScanTierSnapshot(t *tier, after, before int64) ([]blockMeta, []Bucket) {
	sr := t.get("fixture")
	sr.mu.Lock()
	defer sr.mu.Unlock()
	lo := after - t.spec.Every + 1
	blocks := fullScanBlocks(sr.blocks, lo, before)
	mem := make([]Bucket, 0, len(sr.done)+1)
	for _, b := range sr.done {
		if b.TS >= lo && b.TS <= before {
			mem = append(mem, b)
		}
	}
	if sr.open != nil && sr.open.TS >= lo && sr.open.TS <= before {
		mem = append(mem, *sr.open)
	}
	return blocks, mem
}

var snapshotBenchBlocks []blockMeta
var snapshotBenchBuckets []Bucket

func BenchmarkSnapshotWindow(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		sr := &tierSeries{}
		for i := 0; i < n; i++ {
			sr.blocks = append(sr.blocks, blockMeta{start: int64(i * 10), end: int64(i*10 + 9)})
			sr.done = append(sr.done, Bucket{TS: int64((n + i) * 10), Count: 1})
		}
		sr.open = &Bucket{TS: int64(n * 20), Count: 1}
		tier := &tier{spec: TierSpec{Every: 10}, series: map[string]*tierSeries{"fixture": sr}}
		for _, window := range []struct {
			name          string
			after, before int64
		}{
			{"recent", int64(n*20 - 20), int64(n * 20)},
			{"old", 10, 29},
			{"empty", int64(n*20 + 100), int64(n*20 + 200)},
			{"all", 0, int64(n * 20)},
		} {
			for _, mode := range []string{"full_scan", "window_copy"} {
				b.Run(fmt.Sprintf("n=%d/%s/%s", n, window.name, mode), func(b *testing.B) {
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						if mode == "full_scan" {
							snapshotBenchBlocks, snapshotBenchBuckets = fullScanTierSnapshot(tier, window.after, window.before)
						} else {
							snapshotBenchBlocks, snapshotBenchBuckets, _, _ = tier.snapshot("fixture", window.after, window.before)
						}
					}
				})
			}
		}
	}
}
