package collect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestPHPFPMStatusFailureMarksUnsupported(t *testing.T) {
	body := "pool: www\naccepted conn: 10\nlisten queue: 1\nmax listen queue: 2\nidle processes: 3\nactive processes: 1\nslow requests: 4\n"
	mode := "ok"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if mode == "down" {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		if mode == "bad" {
			_, _ = w.Write([]byte("not fpm"))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	p := &phpfpmCollector{cfg: phpfpmConfig{URL: srv.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := p.Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	perf := mustChart(t, reg, "phpfpm.performance")
	queue := mustChart(t, reg, "phpfpm.queue")
	if _, vals := perf.LastValues(); vals["active"] != 1 || perf.Unsupported != "" {
		t.Fatalf("performance %+v %q", vals, perf.Unsupported)
	}
	mode = "down"
	if err := p.Collect(context.Background(), reg, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if perf.Unsupported == "" || queue.Unsupported == "" || perf.LastCollectionTime() != now.Unix() {
		t.Fatalf("performance %q at %d queue %q", perf.Unsupported, perf.LastCollectionTime(), queue.Unsupported)
	}
	mode = "bad"
	if err := p.Collect(context.Background(), reg, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if perf.Unsupported == "" {
		t.Fatal("bad body stayed supported")
	}
	mode = "ok"
	if err := p.Collect(context.Background(), reg, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if perf.Unsupported != "" || queue.Unsupported != "" {
		t.Fatalf("stuck performance %q queue %q", perf.Unsupported, queue.Unsupported)
	}
	if _, vals := perf.LastValues(); vals["active"] != 1 {
		t.Fatalf("recovered %+v", vals)
	}
}
