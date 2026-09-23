package tsdb

import (
	"math"
	"testing"
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
