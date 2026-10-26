package tsdb

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"testing"
)

// BenchmarkRollupResidentHeap measures live memory after GC, not alloc_space.
// Each of 2,000 series approaches both default block limits. Optional profiles
// are captured while the Store is still reachable, never after test cleanup.
func BenchmarkRollupResidentHeap(b *testing.B) {
	for range b.N {
		runtime.GC()
		var baseline runtime.MemStats
		runtime.ReadMemStats(&baseline)
		var specs []TierSpec
		if os.Getenv("MONITOR_TSDB_HEAP_LEGACY_BLOCKS") == "1" {
			specs = []TierSpec{{Every: 60, BlockSize: 1440}, {Every: 3600, BlockSize: 720}}
		}
		s, err := Open(Options{Dir: b.TempDir(), Tiers: specs, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		if err != nil {
			b.Fatal(err)
		}
		for _, tier := range s.tiers {
			for i := range 2000 {
				sr := tier.getOrCreate(fmt.Sprintf("metric.%04d", i))
				for n := range tier.spec.BlockSize {
					tier.append(sr, int64(n+1)*tier.spec.Every, float64(n%100))
				}
			}
		}
		report := func(phase string) {
			runtime.GC()
			var sample runtime.MemStats
			runtime.ReadMemStats(&sample)
			b.ReportMetric(float64(int64(sample.HeapAlloc)-int64(baseline.HeapAlloc)), phase+"-live-B")
			if dir := os.Getenv("MONITOR_TSDB_HEAP_PROFILE_DIR"); dir != "" {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					b.Fatal(err)
				}
				f, err := os.Create(filepath.Join(dir, phase+".pprof"))
				if err != nil {
					b.Fatal(err)
				}
				if err := pprof.WriteHeapProfile(f); err != nil {
					b.Fatal(err)
				}
				if err := f.Close(); err != nil {
					b.Fatal(err)
				}
			}
			runtime.KeepAlive(s)
		}
		report("filled")
		s.revision.Add(1)
		if err := s.Flush(); err != nil {
			b.Fatal(err)
		}
		report("checkpoint")
		for _, tier := range s.tiers {
			if err := tier.flushAll(); err != nil {
				b.Fatal(err)
			}
		}
		report("flushed")
		if err := s.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

// Model the retained in-memory block index at the default 90-day/730-day
// horizons. No data files are created. Block paths are not stored; this is a
// metadata cost model, not a production heap.
func BenchmarkRollupRetainedIndex(b *testing.B) {
	for _, test := range []struct {
		name  string
		sizes [2]int
	}{{"legacy", [2]int{1440, 720}}, {"small", [2]int{120, 48}}, {"balanced", [2]int{360, 168}}, {"conservative", [2]int{720, 360}}} {
		b.Run(test.name, func(b *testing.B) {
			for range b.N {
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				tiers := make([]*tier, 0, 2)
				var count int
				for idx, limit := range test.sizes {
					t, err := newTier(idx+1, TierSpec{Every: []int64{60, 3600}[idx], BlockSize: limit}, b.TempDir())
					if err != nil {
						b.Fatal(err)
					}
					tiers = append(tiers, t)
					blocks := ([]int{90 * 24 * 60, 730 * 24}[idx] + limit - 1) / limit
					for i := range 2000 {
						sr := t.getOrCreate(fmt.Sprintf("metric.%04d", i))
						for n := range blocks {
							start := int64(n*limit) * t.spec.Every
							sr.blocks = append(sr.blocks, blockMeta{start: start, end: start + int64(limit-1)*t.spec.Every, count: limit})
							count++
						}
					}
				}
				runtime.GC()
				runtime.ReadMemStats(&after)
				b.ReportMetric(float64(int64(after.HeapAlloc)-int64(before.HeapAlloc)), "index-live-B")
				b.ReportMetric(float64(count), "blocks/retention")
				runtime.KeepAlive(tiers)
			}
		})
	}
}

// Equal logical history quantifies the extra disk blocks/fsyncs caused by a
// smaller default. Unlike the resident-limit probe, both cases ingest exactly
// 1,440 minute buckets and 720 hourly buckets for each of 16 series.
func BenchmarkRollupHistoryIO(b *testing.B) {
	for _, test := range []struct {
		name  string
		specs []TierSpec
	}{
		{"legacy", []TierSpec{{Every: 60, BlockSize: 1440}, {Every: 3600, BlockSize: 720}}},
		{"small", []TierSpec{{Every: 60, BlockSize: 120}, {Every: 3600, BlockSize: 48}}},
		{"balanced", []TierSpec{{Every: 60, BlockSize: 360}, {Every: 3600, BlockSize: 168}}},
		{"conservative", []TierSpec{{Every: 60, BlockSize: 720}, {Every: 3600, BlockSize: 360}}},
	} {
		b.Run(test.name, func(b *testing.B) {
			for range b.N {
				var blocks, bytes int64
				for idx, spec := range test.specs {
					tier, err := newTier(idx+1, spec, b.TempDir())
					if err != nil {
						b.Fatal(err)
					}
					count := []int{1440, 720}[idx]
					for i := range 16 {
						sr := tier.getOrCreate(fmt.Sprintf("metric.%04d", i))
						for n := range count {
							if tier.append(sr, int64(n+1)*spec.Every, float64(n%100)) {
								if err := tier.flush(sr); err != nil {
									b.Fatal(err)
								}
							}
						}
						if err := tier.flush(sr); err != nil {
							b.Fatal(err)
						}
						blocks += int64(len(sr.blocks))
						for _, block := range sr.blocks {
							bytes += block.size
						}
					}
				}
				b.ReportMetric(float64(blocks), "blocks/history")
				b.ReportMetric(float64(bytes), "disk-B/history")
			}
		})
	}
}
