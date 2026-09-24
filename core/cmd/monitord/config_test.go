package main

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/config"
)

func resetApplyState() {
	applyMu.Lock()
	lastApply = time.Time{}
	pendingApply = nil
	applyMu.Unlock()
}

const baseYAML = "mode: agent\nstream:\n  destinations: [wss://hub:20443]\n  api_key: k1\nglobal:\n  hostname: box-1\n"

func TestApplyAgentConfigRejectsLockedFields(t *testing.T) {
	resetApplyState()
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(path, []byte(baseYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cur, _ := config.Parse([]byte(baseYAML))
	var exited atomic.Bool
	bad := strings.Replace(baseYAML, "mode: agent", "mode: hub", 1)
	if err := applyAgentConfig(path, cur, bad, 1, slog.Default(), func() { exited.Store(true) }); err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("locked mode must be rejected, got %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != baseYAML {
		t.Fatal("file must be untouched on rejection")
	}
	if exited.Load() {
		t.Fatal("exit must not run on rejection")
	}
}

func TestApplyAgentConfigIdempotent(t *testing.T) {
	resetApplyState()
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(path, []byte(baseYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cur, _ := config.Parse([]byte(baseYAML))
	var exited atomic.Bool
	if err := applyAgentConfig(path, cur, baseYAML, 1, slog.Default(), func() { exited.Store(true) }); err != nil {
		t.Fatalf("identical content must be a no-op: %v", err)
	}
	if exited.Load() {
		t.Fatal("identical content must not restart")
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatal("no backup expected for a no-op")
	}
}

func TestApplyAgentConfigWritesBackupAndExits(t *testing.T) {
	resetApplyState()
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(path, []byte(baseYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cur, _ := config.Parse([]byte(baseYAML))
	var exited atomic.Bool
	next := strings.Replace(baseYAML, "hostname: box-1", "hostname: box-renamed", 1)
	if err := applyAgentConfig(path, cur, next, 7, slog.Default(), func() { exited.Store(true) }); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !exited.Load() {
		t.Fatal("exit must run after a successful write")
	}
	got, _ := os.ReadFile(path)
	if string(got) != next {
		t.Fatalf("file = %q", got)
	}
	bak, _ := os.ReadFile(path + ".bak")
	if string(bak) != baseYAML {
		t.Fatalf("backup = %q", bak)
	}
}

func TestApplyAgentConfigDebounces(t *testing.T) {
	resetApplyState()
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(path, []byte(baseYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cur, _ := config.Parse([]byte(baseYAML))
	exit := func() {}
	first := strings.Replace(baseYAML, "hostname: box-1", "hostname: one", 1)
	if err := applyAgentConfig(path, cur, first, 1, slog.Default(), exit); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	second := strings.Replace(baseYAML, "hostname: box-1", "hostname: two", 1)
	err := applyAgentConfig(path, cur, second, 2, slog.Default(), exit)
	if !errors.Is(err, ErrApplyDeferred) {
		t.Fatalf("second apply inside the window must defer, got %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != first {
		t.Fatalf("file must still hold the first apply, got %q", got)
	}
	applyMu.Lock()
	p := pendingApply
	applyMu.Unlock()
	if p == nil || p.yamlText != second || p.rev != 2 {
		t.Fatalf("deferred apply not recorded: %+v", p)
	}
	resetApplyState() // drop the pending slot: its timer must not touch later tests
}
