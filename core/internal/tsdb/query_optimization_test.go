package tsdb

import (
	"encoding/binary"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Every reducer must retain its bucket boundaries and empty-slot behavior.
// Rollup average is sample-weighted, while rollup median uses bucket means.
func TestAggregateStreamingReducers(t *testing.T) {
	points := [][]Point{{
		{TS: 101, Value: 4}, {TS: 102, Value: 2},
		{TS: 103, Value: 5}, {TS: 104, Value: 1},
	}}
	buckets := [][]Bucket{{
		{TS: 101, Min: 2, Max: 4, Sum: 6, Last: 2, Count: 2},
		{TS: 102, Min: 9, Max: 9, Sum: 9, Last: 9, Count: 1},
		{TS: 103, Min: 5, Max: 5, Sum: 5, Last: 5, Count: 1},
	}}
	for _, tc := range []struct {
		fn     GroupFunc
		raw    [2]float64
		rollup [2]float64
	}{
		{GroupAverage, [2]float64{3, 3}, [2]float64{5, 5}},
		{GroupMin, [2]float64{2, 1}, [2]float64{2, 5}},
		{GroupMax, [2]float64{4, 5}, [2]float64{9, 5}},
		{GroupSum, [2]float64{6, 6}, [2]float64{15, 5}},
		{GroupMedian, [2]float64{3, 3}, [2]float64{6, 5}},
		{GroupLast, [2]float64{2, 1}, [2]float64{9, 5}},
	} {
		t.Run(string(tc.fn), func(t *testing.T) {
			for name, got := range map[string]Result{
				"raw":    Aggregate(points, 100, 108, 4, tc.fn),
				"rollup": AggregateBuckets(buckets, 1, 100, 108, 4, tc.fn),
			} {
				want := tc.raw
				if name == "rollup" {
					want = tc.rollup
				}
				if len(got.Values) != 1 || len(got.Values[0]) != 4 ||
					got.Values[0][0] != want[0] || got.Values[0][1] != want[1] ||
					!math.IsNaN(got.Values[0][2]) || !math.IsNaN(got.Values[0][3]) {
					t.Fatalf("%s: got %v, want first two %v and two gaps", name, got.Values, want)
				}
			}
		})
	}
	zeroCount := AggregateBuckets([][]Bucket{{{TS: 101, Sum: 7, Count: 0}}}, 1, 100, 102, 1, GroupAverage)
	if !math.IsNaN(zeroCount.Values[0][0]) {
		t.Fatalf("zero-count rollup must have no average: %v", zeroCount.Values)
	}
}

func TestQueryAggregatedMatchesMaterialized(t *testing.T) {
	s, err := Open(Options{Dir: t.TempDir(), BlockSize: 3, Tiers: []TierSpec{{Every: 60, BlockSize: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	start := time.Now().Unix() - 30
	start -= start % 60
	for i := int64(0); i < 8; i++ {
		s.Append("cpu|user", start+i*60, float64((i%4)+1))
		s.Append("cpu|user", start+i*60+1, float64((i%4)+3))
	}
	for _, tier := range []int{0, 1} {
		for _, fn := range []GroupFunc{GroupAverage, GroupMin, GroupMax, GroupSum, GroupLast, GroupMedian} {
			got, err := s.QueryAggregated("cpu|user", tier, start-1, start+8*60, 4, fn)
			if err != nil {
				t.Fatal(err)
			}
			bs, err := s.QueryTier("cpu|user", tier, start-1, start+8*60)
			if err != nil {
				t.Fatal(err)
			}
			every, _ := s.TierEvery(tier)
			want := AggregateBuckets([][]Bucket{bs}, every, start-1, start+8*60, 4, fn)
			if len(got.Values) != 1 || len(got.Values[0]) != len(want.Values[0]) || got.Step != want.Step {
				t.Fatalf("tier %d %s shape got %v want %v", tier, fn, got.Values, want.Values)
			}
			for i := range want.Values[0] {
				g, w := got.Values[0][i], want.Values[0][i]
				if math.IsNaN(g) && math.IsNaN(w) {
					continue
				}
				if g != w {
					t.Fatalf("tier %d %s [%d] got %v want %v", tier, fn, i, got.Values, want.Values)
				}
			}
		}
	}
}

func TestQueryAggregatedSkipsTruncatedBlock(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, BlockSize: 4, Tiers: []TierSpec{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	start := time.Now().Unix() - 30
	for i := int64(0); i < 4; i++ {
		s.Append("cpu|user", start+i, float64(i+1))
	}
	s.Append("cpu|user", start+4, 100)
	paths := blockPaths(t, dir, "tier0")
	if len(paths) != 1 {
		t.Fatalf("blocks: %v", paths)
	}
	shortenBlockPayload(t, paths[0])
	got, err := s.QueryAggregated("cpu|user", 0, start-1, start+10, 20, GroupSum)
	if err != nil {
		t.Fatal(err)
	}
	if sumFinite(got.Values[0]) != 100 {
		t.Fatalf("truncated raw block leaked: %v", got.Values)
	}
}

func TestQueryAggregatedSkipsTruncatedTierBlock(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, BlockSize: 100, Tiers: []TierSpec{{Every: 60, BlockSize: 2}}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	start := time.Now().Unix() - 300
	start -= start % 60
	for _, ts := range []int64{0, 60} {
		s.Append("cpu|user", start+ts, 4)
		s.Append("cpu|user", start+ts+1, 4)
	}
	s.Append("cpu|user", start+120, 10)
	paths := blockPaths(t, dir, "tier1")
	if len(paths) != 1 {
		t.Fatalf("tier blocks: %v", paths)
	}
	shortenBlockPayload(t, paths[0])
	got, err := s.QueryAggregated("cpu|user", 1, start-1, start+180, 8, GroupSum)
	if err != nil {
		t.Fatal(err)
	}
	if sumFinite(got.Values[0]) != 10 {
		t.Fatalf("truncated tier block leaked: %v", got.Values)
	}
}

func blockPaths(t *testing.T, root, tier string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".blk") {
			return nil
		}
		if strings.Contains(p, tier+string(filepath.Separator)) || strings.Contains(p, "/"+tier+"/") {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func shortenBlockPayload(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	idLen := int(binary.LittleEndian.Uint16(b[5:7]))
	off := 4 + 1 + 2 + idLen + 8 + 8 + 4
	dataLen := binary.LittleEndian.Uint32(b[off : off+4])
	if dataLen < 2 || int(dataLen) > len(b)-off-4 {
		t.Fatalf("payload %d file %d", dataLen, len(b))
	}
	binary.LittleEndian.PutUint32(b[off:], dataLen-1)
	if err := os.WriteFile(path, b[:len(b)-1], 0o640); err != nil {
		t.Fatal(err)
	}
}

func sumFinite(v []float64) float64 {
	var s float64
	for _, x := range v {
		if !math.IsNaN(x) {
			s += x
		}
	}
	return s
}
