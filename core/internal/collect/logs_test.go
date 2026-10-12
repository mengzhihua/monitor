package collect

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func TestLogsInitWithoutJournaldStillEnablesFunction(t *testing.T) {
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{ID: "id", Hostname: "h", OS: "linux", UpdateEvery: 1}, db)
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("Oct 12 08:00:00 host app: hello from fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := &logsCollector{}
	if err := l.Configure(func(v any) error {
		cfg := v.(*logsConfig)
		cfg.Follow = boolPtr(false)
		cfg.Files = []string{path}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.Init(reg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Stop)
	fns := l.Functions()
	if len(fns) != 1 || fns[0].Name != "logs" {
		t.Fatalf("functions = %+v", fns)
	}
	res, err := fns[0].Run(t.Context(), map[string]string{"source": "file"})
	if err != nil {
		t.Fatal(err)
	}
	tab, ok := res.(Table)
	if !ok || tab.Total < 1 {
		t.Fatalf("result = %#v", res)
	}
	// Empty-source Init must also succeed (no journald / no files).
	empty := &logsCollector{}
	_ = empty.Configure(func(v any) error {
		cfg := v.(*logsConfig)
		cfg.Follow = boolPtr(false)
		cfg.Files = []string{filepath.Join(dir, "missing.log")}
		return nil
	})
	if err := empty.Init(reg); err != nil {
		t.Fatalf("empty sources should still Init: %v", err)
	}
	t.Cleanup(empty.Stop)
}

func boolPtr(v bool) *bool { return &v }

func TestSchedulerRecordsPerCollectorTiming(t *testing.T) {
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{ID: "id", Hostname: "h", OS: "linux", UpdateEvery: 1}, db)
	s := NewScheduler(reg, nil, Options{Names: []string{"checks"}})
	s.tick(t.Context(), time.Now().Truncate(time.Second))
	// Allow the Collect goroutine to finish.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, st := range s.Status() {
			if st.Name == "checks" && st.Runs > 0 {
				if st.TotalRun < 0 || st.MaxRun < 0 {
					t.Fatalf("bad timing %+v", st)
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("checks did not record a run")
}

func TestParseLogcat(t *testing.T) {
	raw := `--------- beginning of main
01-02 15:04:05.123  1234  5678 I Monitor: agent started
01-02 15:04:06.000   101   101 E zygote  : failed to fork
1750000000.500  W/System  (  42): low memory
I/okhttp  (  99): hello
`
	rows := parseLogcat(raw, LogQuery{Limit: 50})
	if len(rows) < 3 {
		t.Fatalf("rows = %+v", rows)
	}
	found := map[string]bool{}
	for _, r := range rows {
		found[r.Unit] = true
		if r.Message == "" {
			t.Fatalf("empty message %+v", r)
		}
	}
	if !found["Monitor"] || !found["zygote"] {
		t.Fatalf("units = %v", found)
	}
	filtered := parseLogcat(raw, LogQuery{Limit: 50, Query: "fork"})
	if len(filtered) != 1 || filtered[0].Priority != "err" {
		t.Fatalf("filter = %+v", filtered)
	}
}

func TestParseEventLogText(t *testing.T) {
	raw := "Event[0]:\n  Log: Application\n  Source: Monitor\n  Level: Warning\n  Date: 2024-01-02T03:04:05.000\n  Description: disk full\n\nEvent[1]:\n  Log: System\n  Level: Information\n  Description: started\n"
	rows := parseEventLogText(raw, LogQuery{Limit: 10})
	if len(rows) != 2 || rows[0].Unit != "Application" || rows[0].Priority != "warning" || rows[0].Message != "disk full" {
		t.Fatalf("%+v", rows)
	}
	filtered := parseEventLogText(raw, LogQuery{Limit: 10, Query: "disk"})
	if len(filtered) != 1 {
		t.Fatalf("filter = %+v", filtered)
	}
}
