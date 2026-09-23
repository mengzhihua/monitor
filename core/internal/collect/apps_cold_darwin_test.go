//go:build darwin

package collect

import (
	"context"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// Measures the first full collection on the live host, with fresh identity and
// owner caches each time. It intentionally has no one-second scheduler limit.
// Setup is excluded; process count and other host activity can vary between runs.
func BenchmarkAppsDarwinColdCollect(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
		a := &appsCollector{}
		if err := a.Init(reg); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		err := a.Collect(context.Background(), reg, time.Now())
		b.StopTimer()
		if err != nil {
			b.Fatal(err)
		}
		b.ReportMetric(float64(len(a.pids)), "pids/op")
	}
}

func BenchmarkAppsDarwinSteadyCollect(b *testing.B) {
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	a := &appsCollector{}
	if err := a.Init(reg); err != nil {
		b.Fatal(err)
	}
	now := time.Now()
	if err := a.Collect(context.Background(), reg, now); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := a.Collect(context.Background(), reg, now.Add(time.Duration(i+1)*time.Second)); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(len(a.pids)), "pids/op")
}
