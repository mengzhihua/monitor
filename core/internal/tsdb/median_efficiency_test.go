package tsdb

import (
	"math"
	"reflect"
	"sort"
	"testing"
)

func BenchmarkMedianAggregation(b *testing.B) {
	// One day of raw samples and one week of one-minute rollups, both
	// displayed in a 600-point chart. Inputs are built outside the timer.
	b.Run("raw_day", func(b *testing.B) {
		pts := make([]Point, 86400)
		for i := range pts {
			pts[i] = Point{TS: int64(i + 1), Value: float64((i * 37) % 1009)}
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			r := Aggregate([][]Point{pts}, 0, 86400, 600, GroupMedian)
			if len(r.Values[0]) != 600 {
				b.Fatal("unexpected grid")
			}
		}
	})
	b.Run("rollup_week", func(b *testing.B) {
		bs := make([]Bucket, 7*24*60)
		for i := range bs {
			bs[i] = Bucket{TS: int64(i * 60), Sum: float64((i * 37) % 1009), Count: int64(i%7 + 1)}
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			r := AggregateBuckets([][]Bucket{bs}, 60, 0, 7*86400, 600, GroupMedian)
			if len(r.Values[0]) != 600 {
				b.Fatal("unexpected grid")
			}
		}
	})
}

func TestMedianAggregationPreservesValuesAndInputs(t *testing.T) {
	for _, values := range [][]float64{
		{7}, {9, 1}, {9, -2, 5}, {8, 8, -4, 2},
		{math.NaN(), 3, 1}, {math.Inf(-1), 2, math.Inf(1)},
	} {
		ordered := append([]float64(nil), values...)
		sort.Float64s(ordered)
		want := ordered[len(ordered)/2]
		if len(ordered)%2 == 0 {
			want = (ordered[len(ordered)/2-1] + want) / 2
		}
		pts := make([]Point, len(values))
		bs := make([]Bucket, len(values))
		for i, v := range values {
			pts[i] = Point{TS: int64(i + 1), Value: v}
			// Different counts verify median-of-means, not weighted median.
			bs[i] = Bucket{TS: int64(i + 1), Sum: v * float64(i+1), Count: int64(i + 1)}
		}
		end := int64(2 * len(values))
		for name, r := range map[string]Result{
			"raw":    Aggregate([][]Point{pts, {{TS: 1, Value: 99}}}, 0, end, 2, GroupMedian),
			"rollup": AggregateBuckets([][]Bucket{bs, {{TS: 1, Sum: 99, Count: 1}}}, 1, 0, end, 2, GroupMedian),
		} {
			got := r.Values[0][0]
			if !(got == want || math.IsNaN(got) && math.IsNaN(want)) || !math.IsNaN(r.Values[0][1]) || r.Values[1][0] != 99 || !math.IsNaN(r.Values[1][1]) {
				t.Fatalf("%s values=%v got=%v want=%v", name, values, r.Values, want)
			}
		}
		for i, v := range values {
			if math.Float64bits(pts[i].Value) != math.Float64bits(v) || math.Float64bits(bs[i].Sum) != math.Float64bits(v*float64(i+1)) {
				t.Fatal("aggregation mutated caller input")
			}
		}
	}
}

func TestMedianFoldRestoresPartialBlock(t *testing.T) {
	for _, rollup := range []bool{false, true} {
		f := newFold(QueryGrid(0, 8, 2), GroupMedian)
		add := func(ts int64, v float64) {
			if rollup {
				f.addBucket(Bucket{TS: ts, Sum: v * 2, Count: 2}, 1)
			} else {
				f.addPoint(ts, v)
			}
		}
		f.save()
		add(1, 1000)
		f.restore() // First corrupt block must leave no samples behind.
		add(1, 2)
		add(2, 8)
		f.save()
		add(3, -1000)
		add(6, -1000)
		f.restore() // Corruption after a valid block must retain its values.
		add(7, 3)
		if got := f.finish(); !reflect.DeepEqual(got, []float64{5, 3}) {
			t.Fatalf("rollup=%v got=%v", rollup, got)
		}
	}
}
