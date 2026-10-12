package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func TestCollectorTimingExposedOnMetricsAndStatus(t *testing.T) {
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{ID: "id", Hostname: "timing-host", OS: "linux", UpdateEvery: 1}, db)
	sched := collect.NewScheduler(reg, nil, collect.Options{Names: []string{"checks"}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, st := range sched.Status() {
			if st.Name == "checks" && st.Enabled && st.Runs > 0 {
				goto ready
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("checks collector did not run")
ready:
	srv, err := New(reg, db, sched, Options{Token: "timing-token", StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer timing-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)
	if !strings.Contains(text, `monitor_collector_last_run_ms{collector="checks"`) {
		t.Fatalf("missing checks last_run metric: %s", text[:min(600, len(text))])
	}
	if !strings.Contains(text, `monitor_collector_max_run_ms{collector="checks"`) {
		t.Fatalf("missing checks max_run metric")
	}
	if !strings.Contains(text, `monitor_collector_runs{collector="checks"`) {
		t.Fatalf("missing checks runs metric")
	}

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/v1/collectors", nil)
	req.Header.Set("Authorization", "Bearer timing-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Status []collect.Status `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	var checks *collect.Status
	for i := range out.Status {
		if out.Status[i].Name == "checks" {
			checks = &out.Status[i]
			break
		}
	}
	if checks == nil || checks.Runs == 0 {
		t.Fatalf("checks status missing: %+v", out.Status)
	}
	if checks.TotalRun < 0 || checks.MaxRun < checks.LastRun {
		t.Fatalf("timing fields inconsistent: %+v", checks)
	}
}
