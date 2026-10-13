package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/audit"
	"github.com/mengzhihua/monitor/core/internal/config"
	"github.com/mengzhihua/monitor/core/internal/health"
)

func pythonAbs(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"python3", "python"} {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		if abs, err := filepath.Abs(path); err == nil && filepath.IsAbs(abs) {
			return abs
		}
		if filepath.IsAbs(path) {
			return path
		}
	}
	t.Skip("python3/python not found")
	return ""
}

func TestAutoCommandRunnerRecordsExitAndOutput(t *testing.T) {
	py := pythonAbs(t)
	log, err := audit.Open(t.TempDir(), 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })

	var mu sync.Mutex
	var gotExit int
	var gotDur int64
	var gotOut string
	var gotInv health.ActionCommand
	r := &autoCommandRunner{
		commands: []config.Command{{Name: "fix", Argv: []string{py, "-c", "import sys; print('hello-auto'); sys.exit(3)"}}},
		audit:    log,
		timeout:  5 * time.Second,
		record: func(inv health.ActionCommand, exit int, durationMs int64, output string) {
			mu.Lock()
			defer mu.Unlock()
			gotInv, gotExit, gotDur, gotOut = inv, exit, durationMs, output
		},
	}
	r.Run(health.ActionCommand{
		Name: "fix", AlarmID: 9, AlarmName: "disk_full", Chart: "disk.space",
		EpisodeStatus: health.StatusCritical, EpisodeSince: 100,
	})

	mu.Lock()
	defer mu.Unlock()
	if gotExit != 3 || gotDur < 0 || !strings.Contains(gotOut, "hello-auto") {
		t.Fatalf("result exit=%d dur=%d out=%q", gotExit, gotDur, gotOut)
	}
	if gotInv.AlarmID != 9 || gotInv.Name != "fix" {
		t.Fatalf("inv %+v", gotInv)
	}
	entries := log.Query(0, 10, "system")
	if len(entries) != 1 {
		t.Fatalf("audit entries %+v", entries)
	}
	e := entries[0]
	if e.Action != "action:fix" || e.ExitCode == nil || *e.ExitCode != 3 || e.Output == "" {
		t.Fatalf("audit entry %+v", e)
	}
	if e.Status != 500 {
		t.Fatalf("non-zero exit should audit as 500, got %d", e.Status)
	}
	if e.DurationMs <= 0 {
		t.Fatalf("duration missing: %+v", e)
	}
	if !strings.Contains(e.Note, "ok=false") {
		t.Fatalf("note %q", e.Note)
	}

}

func TestAutoCommandRunnerTimesOut(t *testing.T) {
	py := pythonAbs(t)
	log, err := audit.Open(t.TempDir(), 50)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	r := &autoCommandRunner{
		commands: []config.Command{{Name: "slow", Argv: []string{py, "-c", "import time; time.sleep(5)"}}},
		timeout:  200 * time.Millisecond,
		audit:    log,
	}
	var exit int
	var dur int64
	r.record = func(_ health.ActionCommand, code int, durationMs int64, _ string) { exit, dur = code, durationMs }
	started := time.Now()
	r.Run(health.ActionCommand{Name: "slow", AlarmName: "x", Chart: "y"})
	if time.Since(started) > 2*time.Second {
		t.Fatalf("timeout took too long: %s", time.Since(started))
	}
	if exit == 0 || dur <= 0 {
		t.Fatalf("exit=%d dur=%d", exit, dur)
	}
	entries := log.Query(0, 10, "system")
	if len(entries) == 0 || entries[len(entries)-1].Status != 504 {
		t.Fatalf("timeout should audit as 504: %+v", entries)
	}
}

func TestAutoCommandPerCommandTimeout(t *testing.T) {
	py := pythonAbs(t)
	r := &autoCommandRunner{
		commands: []config.Command{{
			Name: "slow", Timeout: 150 * time.Millisecond,
			Argv: []string{py, "-c", "import time; time.sleep(5)"},
		}},
		timeout: 30 * time.Second, // must not win over the per-command timeout
	}
	var exit int
	started := time.Now()
	r.record = func(_ health.ActionCommand, code int, _ int64, _ string) { exit = code }
	r.Run(health.ActionCommand{Name: "slow", AlarmName: "x", Chart: "y"})
	if time.Since(started) > 2*time.Second {
		t.Fatalf("per-command timeout ignored: %s", time.Since(started))
	}
	if exit == 0 {
		t.Fatalf("exit=%d", exit)
	}
}

func TestAutoCommandRunnerUnknownCommand(t *testing.T) {
	log, err := audit.Open(t.TempDir(), 50)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	var exit int
	var out string
	r := &autoCommandRunner{audit: log, record: func(_ health.ActionCommand, code int, _ int64, output string) {
		exit, out = code, output
	}}
	r.Run(health.ActionCommand{Name: "missing", AlarmName: "a", Chart: "b"})
	if exit != 127 || out == "" {
		t.Fatalf("exit=%d out=%q", exit, out)
	}
}

func TestAutoCommandTruncatesOutput(t *testing.T) {
	py := pythonAbs(t)
	var out string
	r := &autoCommandRunner{
		commands: []config.Command{{Name: "big", Argv: []string{py, "-c", "print('x'*8000)"}}},
		timeout:  5 * time.Second,
		record:   func(_ health.ActionCommand, _ int, _ int64, output string) { out = output },
	}
	r.Run(health.ActionCommand{Name: "big", AlarmName: "a", Chart: "b"})
	if len(out) != autoCommandOutputMax {
		t.Fatalf("len=%d want %d", len(out), autoCommandOutputMax)
	}
}
