package main

import (
	"os"
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

func TestAutoCommandRunnerRecordsExitAndOutput(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fix.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hello-auto\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	log, err := audit.Open(dir, 100)
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
		commands: []config.Command{{Name: "fix", Argv: []string{script}}},
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
	found := false
	for _, e := range entries {
		if e.Action == "action:fix" && e.ExitCode != nil && *e.ExitCode == 3 && e.Output != "" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("audit missing auto action: %+v", entries)
	}
}

func TestAutoCommandRunnerTimesOut(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not found")
	}
	if !filepath.IsAbs(sleep) {
		t.Skip("sleep path not absolute")
	}
	r := &autoCommandRunner{
		commands: []config.Command{{Name: "slow", Argv: []string{sleep, "5"}}},
		timeout:  200 * time.Millisecond,
	}
	var exit int
	r.record = func(_ health.ActionCommand, code int, _ int64, _ string) { exit = code }
	started := time.Now()
	r.Run(health.ActionCommand{Name: "slow", AlarmName: "x", Chart: "y"})
	if time.Since(started) > 2*time.Second {
		t.Fatalf("timeout took too long: %s", time.Since(started))
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
	dir := t.TempDir()
	script := filepath.Join(dir, "big.sh")
	// Avoid a huge source file: print many lines from the shell.
	if err := os.WriteFile(script, []byte("#!/bin/sh\npython3 -c 'print(\"x\"*8000)'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required")
	}
	var out string
	r := &autoCommandRunner{
		commands: []config.Command{{Name: "big", Argv: []string{script}}},
		timeout:  5 * time.Second,
		record:   func(_ health.ActionCommand, _ int, _ int64, output string) { out = output },
	}
	r.Run(health.ActionCommand{Name: "big", AlarmName: "a", Chart: "b"})
	if len(out) != autoCommandOutputMax {
		t.Fatalf("len=%d want %d", len(out), autoCommandOutputMax)
	}
}
