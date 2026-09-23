package api

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

// Use the actual ML collector so each registered metric participates in its
// anomaly snapshot, including normal dimensions omitted from the result map.
func benchmarkMetadataServer(b *testing.B, charts int) *Server {
	b.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := tsdb.Open(tsdb.Options{Dir: b.TempDir(), Logger: log})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{ID: "local", Hostname: "benchmark", UpdateEvery: 1}, nil)
	sched := collect.NewScheduler(reg, log, collect.Options{Names: []string{"ml"}})
	for i := 0; i < charts; i++ {
		chart := &registry.Chart{ID: fmt.Sprintf("test.chart%d", i), Context: "test.shared"}
		values := make(map[string]float64, 8)
		for j := 0; j < 8; j++ {
			id := fmt.Sprintf("dim%d", j)
			chart.Dimensions = append(chart.Dimensions, &registry.Dimension{ID: id})
			values[id] = float64(j)
		}
		reg.AddChart(chart)
		if err := reg.Collect(chart.ID, time.Unix(1700000000, 0), values); err != nil {
			b.Fatal(err)
		}
	}
	srv, err := New(reg, db, sched, Options{Logger: log})
	if err != nil {
		b.Fatal(err)
	}
	return srv
}

type discardResponse struct{ header http.Header }

func (w discardResponse) Header() http.Header       { return w.header }
func (discardResponse) WriteHeader(int)             {}
func (discardResponse) Write(p []byte) (int, error) { return len(p), nil }

func BenchmarkMetadata(b *testing.B) {
	for _, charts := range []int{250, 1000} {
		b.Run(fmt.Sprintf("charts=%d", charts), func(b *testing.B) {
			srv := benchmarkMetadataServer(b, charts)
			for _, route := range []struct {
				name string
				fn   http.HandlerFunc
			}{{"charts", srv.handleCharts}, {"contexts", srv.handleContexts}} {
				b.Run(route.name, func(b *testing.B) {
					request := httptest.NewRequest(http.MethodGet, "/api/v1/"+route.name, nil)
					writer := discardResponse{header: make(http.Header)}
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						route.fn(writer, request)
					}
				})
			}
		})
	}
}

func BenchmarkLiveBroadcast(b *testing.B) {
	for _, subscribed := range []bool{false, true} {
		b.Run(fmt.Sprintf("subscribed=%t", subscribed), func(b *testing.B) {
			chart := "system.cpu"
			if !subscribed {
				chart = "system.ram"
			}
			c := &liveConn{charts: map[string]bool{chart: true}, send: make(chan []byte, 1)}
			h := &liveHub{conns: map[*liveConn]struct{}{c: {}}}
			values := map[string]float64{"user": 12, "system": 4, "idle": 84, "wait": 0, "nice": 0, "irq": 0, "softirq": 0, "steal": 0}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				h.broadcast("system.cpu", 1700000000, values)
				if subscribed {
					<-c.send
				}
			}
		})
	}
}
