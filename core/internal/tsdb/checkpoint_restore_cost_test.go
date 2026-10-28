package tsdb

import (
	"io"
	"log/slog"
	"testing"
)

func restoreCostFixture(n int) (checkpointImage, func() *Store) {
	ts, values, buckets := make([]int64, n), make([]float64, n), make([]Bucket, n)
	for i := range n {
		ts[i], values[i] = int64(i+1), float64(i+1)
		buckets[i] = Bucket{TS: int64(i+1) * 60, Count: 1, Sum: float64(i + 1), Min: float64(i + 1), Max: float64(i + 1), Last: float64(i + 1)}
	}
	image := checkpointImage{Version: 1,
		Raw:   []rawCheckpoint{{ID: "x", Last: int64(n), TS: ts, Values: values}},
		Tiers: []tierCheckpoint{{Every: 60, Series: []rollupCheckpoint{{ID: "x", Done: buckets}}}},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return image, func() *Store {
		return &Store{opt: Options{BlockSize: 32}, log: log, series: map[string]*series{},
			tiers: []*tier{{spec: TierSpec{Every: 60, BlockSize: 16}, series: map[string]*tierSeries{}}}}
	}
}

func TestCheckpointRestoreAllocationCostDoesNotGrowPerSample(t *testing.T) {
	image, newStore := restoreCostFixture(2048)
	allocs := testing.AllocsPerRun(1, func() {
		s := newStore()
		if err := s.applyCheckpointImage(image); err != nil {
			t.Fatal(err)
		}
	})
	// Restoring an older, larger block must not allocate/copy a full buffer for
	// every point beyond the new limit. Leave ample room for maps and logging.
	if allocs > 100 {
		t.Fatalf("restoring one series made %.0f allocations; expected bounded batch allocation", allocs)
	}
	s := newStore()
	// Already persisted prefixes are excluded, without reserving space for them.
	s.series["x"] = &series{id: "x", last: 1024}
	s.tiers[0].series["x"] = &tierSeries{id: "x", blocks: []blockMeta{{end: 1024 * 60}}}
	if err := s.applyCheckpointImage(image); err != nil {
		t.Fatal(err)
	}
	raw, rollup := s.series["x"], s.tiers[0].series["x"]
	if len(raw.ts) != 1024 || cap(raw.ts) != 1024 || len(raw.vals) != 1024 || cap(raw.vals) != 1024 || len(rollup.done) != 1024 || cap(rollup.done) != 1024 {
		t.Fatalf("restore retained duplicate prefix/capacity: raw=%d/%d vals=%d/%d rollup=%d/%d", len(raw.ts), cap(raw.ts), len(raw.vals), cap(raw.vals), len(rollup.done), cap(rollup.done))
	}
	for i := range raw.ts {
		want := int64(1025 + i)
		if raw.ts[i] != want || raw.vals[i] != float64(want) || rollup.done[i] != image.Tiers[0].Series[0].Done[1024+i] {
			t.Fatalf("restored data differs at %d", i)
		}
	}
}

func BenchmarkCheckpointRestoreSmallerBlocks(b *testing.B) {
	image, newStore := restoreCostFixture(2048)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := newStore().applyCheckpointImage(image); err != nil {
			b.Fatal(err)
		}
	}
}
