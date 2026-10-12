package export

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestExportSnapshotDoesNotRetimestampMissingDimensions(t *testing.T) {
	reg := registry.New(&registry.Host{Hostname: "test", UpdateEvery: 1}, nil)
	reg.AddChart(&registry.Chart{ID: "memory", Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "free"}}})
	if err := reg.Collect("memory", time.Unix(100, 0), map[string]float64{"used": 75, "free": 25}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Collect("memory", time.Unix(101, 0), map[string]float64{"free": 30}); err != nil {
		t.Fatal(err)
	}
	e := New(reg, nil, nil)
	points := e.snapshot()
	if len(points) != 1 || points[0].dim != "free" || points[0].ts != 101 || points[0].value != 30 {
		t.Fatalf("stale samples exported: %+v", points)
	}
	// JSON takes a separate chart-shaped path. Exercise real HTTP delivery too.
	bodies := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	if err := e.flushJSON(context.Background(), Destination{URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	body := <-bodies
	if !json.Valid(body) || strings.Contains(string(body), "used") || !strings.Contains(string(body), `"free":30`) || !strings.Contains(string(body), `"last_updated":101`) {
		t.Fatalf("stale JSON export: %s", body)
	}
	// An entirely missing collection must not revive any cached dimension.
	if err := reg.Collect("memory", time.Unix(102, 0), nil); err != nil {
		t.Fatal(err)
	}
	if points := e.snapshot(); len(points) != 0 {
		t.Fatalf("empty collection replayed old values: %+v", points)
	}
}
