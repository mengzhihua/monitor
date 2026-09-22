package tsdb

import (
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"
)

func BenchmarkAppend2000Series(b *testing.B) {
	s, err := Open(Options{Dir: b.TempDir(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	ids := make([]string, 2000)
	for i := range ids {
		ids[i] = fmt.Sprintf("metric.%d", i)
	}
	start := time.Now().Unix()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Append(ids[i%len(ids)], start+int64(i/len(ids)), float64(i%100))
	}
	b.StopTimer()
	if err := s.Flush(); err != nil {
		b.Fatal(err)
	}
}
func BenchmarkQuery24Hours600Points(b *testing.B) {
	s, err := Open(Options{Dir: b.TempDir(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	start := time.Now().Unix() - 86400
	for i := int64(0); i < 86400; i++ {
		s.Append("cpu", start+i, float64(i%100))
	}
	if err := s.Flush(); err != nil {
		b.Fatal(err)
	}
	tier := s.PlanTier(start, start+86399, 600)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buckets, err := s.QueryTier("cpu", tier, start, start+86399)
		if err != nil {
			b.Fatal(err)
		}
		every, _ := s.TierEvery(tier)
		result := AggregateBuckets([][]Bucket{buckets}, every, start, start+86399, 600, GroupAverage)
		if len(result.Times) == 0 {
			b.Fatal("empty query")
		}
	}
}

func BenchmarkCheckpoint2000Series(b *testing.B) {
	s, err := Open(Options{Dir: b.TempDir(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	ids := make([]string, 2000)
	for i := range ids {
		ids[i] = fmt.Sprintf("metric.%d", i)
	}
	start := time.Now().Unix()
	b.ResetTimer()
	for round := 0; round < b.N; round++ {
		b.StopTimer()
		for _, id := range ids {
			s.Append(id, start+int64(round), 1)
		}
		b.StartTimer()
		if err := s.Flush(); err != nil {
			b.Fatal(err)
		}
	}
}
