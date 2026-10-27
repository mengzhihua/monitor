package collect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestApacheStatusFailureMarksUnsupported(t *testing.T) {
	body := "Total Accesses: 42\nTotal kBytes: 8\nBusyWorkers: 3\nIdleWorkers: 7\n" +
		"ConnsTotal: 2\nScoreboard: ___W_K..\n"
	mode := "ok"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if mode == "down" {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		if mode == "bad" {
			_, _ = w.Write([]byte("not apache"))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	a := &apacheCollector{cfg: apacheConfig{URL: srv.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := a.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := a.Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	workers := mustChart(t, reg, "apache.workers")
	conns := mustChart(t, reg, "apache.connections")
	if _, vals := workers.LastValues(); vals["busy"] != 3 || workers.Unsupported != "" {
		t.Fatalf("workers %+v %q", vals, workers.Unsupported)
	}
	mode = "down"
	if err := a.Collect(context.Background(), reg, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if workers.Unsupported == "" || conns.Unsupported == "" || workers.LastCollectionTime() != now.Unix() {
		t.Fatalf("workers %q at %d connections %q", workers.Unsupported, workers.LastCollectionTime(), conns.Unsupported)
	}
	mode = "bad"
	if err := a.Collect(context.Background(), reg, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if workers.Unsupported == "" {
		t.Fatal("bad body stayed supported")
	}
	mode = "ok"
	if err := a.Collect(context.Background(), reg, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if workers.Unsupported != "" || conns.Unsupported != "" {
		t.Fatalf("stuck workers %q connections %q", workers.Unsupported, conns.Unsupported)
	}
	if _, vals := workers.LastValues(); vals["busy"] != 3 {
		t.Fatalf("recovered %+v", vals)
	}
}
