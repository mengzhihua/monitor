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
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

// Exercise the real ML collector through all data API versions. Keep a plain
// history of observed bits as the oracle, including ring eviction and shared
// inclusive bucket endpoints used by the existing HTTP contract.
func TestAnomalyHistoryRealCollector(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir(), Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{ID: "fixture", UpdateEvery: 1}, db)
	reg.AddChart(&registry.Chart{ID: "test.signal", Dimensions: []*registry.Dimension{{ID: "value"}}})
	sched := collect.NewScheduler(reg, log, collect.Options{Names: []string{"ml"}})
	source, ok := sched.Collector("ml").(health.AnomalySource)
	if !ok {
		t.Fatal("real ML collector unavailable")
	}
	srv, err := New(reg, db, sched, Options{Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	const start = int64(1700000000)
	type bit struct {
		ts   int64
		rate float64
	}
	var history []bit
	for i := 0; i < 180; i++ {
		value := 10.0
		if i == 175 {
			value = 1000
		}
		timestamp := start + int64(i)
		if err := reg.Collect("test.signal", time.Unix(timestamp, 0), map[string]float64{"value": value}); err != nil {
			t.Fatal(err)
		}
		rate, ok := source.Rate("test.signal", "value")
		if !ok {
			t.Fatal("missing observed anomaly bit")
		}
		history = append(history, bit{timestamp, rate})
	}
	history = history[len(history)-120:] // default ML history capacity
	found := false
	for _, b := range history {
		found = found || b.rate > 0
	}
	if !found {
		t.Fatal("fixture did not produce an anomaly")
	}
	for _, version := range []string{"v1", "v2", "v3"} {
		for _, points := range []int{18, 180} {
			var got struct {
				Units  string
				Result struct{ Data [][]float64 }
			}
			url := fmt.Sprintf("%s/api/%s/data?chart=test.signal&after=%d&before=%d&points=%d&options=anomaly-bit", ts.URL, version, start-1, start+179, points)
			if response := getJSON(t, url, &got); response.StatusCode != http.StatusOK {
				t.Fatalf("%s: status %d", version, response.StatusCode)
			}
			if got.Units != "%" || len(got.Result.Data) == 0 {
				t.Fatalf("%s: empty anomaly response", version)
			}
			step := int64(180 / points)
			for i, row := range got.Result.Data {
				if len(row) != 2 {
					t.Fatalf("%s: malformed row", version)
				}
				end := int64(row[0])
				begin := end - step
				if i > 0 {
					begin = int64(got.Result.Data[i-1][0])
				}
				sum, count := 0.0, 0
				for _, b := range history {
					if b.ts >= begin && b.ts <= end {
						sum += b.rate
						count++
					}
				}
				want := 0.0
				if count > 0 {
					want = round3(sum / float64(count))
				}
				if row[1] != want {
					t.Fatalf("%s points=%d bucket=[%d,%d] got=%v want=%v", version, points, begin, end, row[1], want)
				}
			}
		}
	}
}
