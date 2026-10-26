package tsdb

import (
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"testing"
)

func TestDefaultRawBlockIsFifteenMinutes(t *testing.T) {
	if DefaultBlockSize != 900 {
		t.Fatalf("default raw block = %d", DefaultBlockSize)
	}
	s, err := Open(Options{Dir: t.TempDir(), Tiers: []TierSpec{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	start := int64(1_700_000_000)
	for i := int64(0); i < int64(DefaultBlockSize); i++ {
		s.Append("x", start+i, float64(i))
	}
	s.mu.RLock()
	sr := s.series["x"]
	s.mu.RUnlock()
	sr.mu.Lock()
	n, blocks := len(sr.ts), len(sr.blocks)
	sr.mu.Unlock()
	if n != 0 || blocks != 1 {
		t.Fatalf("after %d samples: active=%d blocks=%d", DefaultBlockSize, n, blocks)
	}
	pts, err := s.Query("x", start, start+int64(DefaultBlockSize))
	if err != nil || len(pts) != DefaultBlockSize {
		t.Fatalf("query %d %v", len(pts), err)
	}
}

// liveRawBufferHeap is the retained heap of `series` active buffers filled to
// samples points and not yet archived. Tiers are off so rollup buckets are
// not part of the number.
func liveRawBufferHeap(dir string, blockSize, series, samples int) (uint64, error) {
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	s, err := Open(Options{Dir: dir, BlockSize: blockSize, Tiers: []TierSpec{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		return 0, err
	}
	start := int64(1_700_000_000)
	for i := range series {
		id := fmt.Sprintf("metric.%04d", i)
		for n := range samples {
			s.Append(id, start+int64(n), float64(n))
		}
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(s)
	if err := s.Close(); err != nil {
		return 0, err
	}
	if after.HeapAlloc < before.HeapAlloc {
		return 0, nil
	}
	return after.HeapAlloc - before.HeapAlloc, nil
}

func TestShorterRawBlocksLowerLiveHeap(t *testing.T) {
	const series = 200
	hour, err := liveRawBufferHeap(t.TempDir(), 3600, series, 3599)
	if err != nil {
		t.Fatal(err)
	}
	quarter, err := liveRawBufferHeap(t.TempDir(), 900, series, 899)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("200 series live heap: 3599 samples=%d B, 899 samples=%d B", hour, quarter)
	if quarter == 0 || hour < quarter*2 {
		t.Fatalf("expected the 15-minute buffer to use less than half the hour buffer: hour=%d quarter=%d", hour, quarter)
	}
}
