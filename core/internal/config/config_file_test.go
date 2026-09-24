package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseValidAndInvalid(t *testing.T) {
	c, err := Parse([]byte("mode: hub\nglobal:\n  hostname: box-1\n"))
	if err != nil {
		t.Fatalf("valid yaml: %v", err)
	}
	if c.Mode != "hub" || c.Global.Hostname != "box-1" {
		t.Fatalf("got mode=%q hostname=%q", c.Mode, c.Global.Hostname)
	}
	if c, err = Parse(nil); err != nil || c.Mode != "agent" {
		t.Fatalf("empty input should yield defaults, got %v %v", c, err)
	}
	if _, err = Parse([]byte("mode: [broken")); err == nil {
		t.Fatal("broken yaml should fail")
	}
	if _, err = Parse([]byte("mode: satellite")); err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("unknown mode should fail with a mode error, got %v", err)
	}
}

func TestSaveWritesContentAndBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	if err := os.WriteFile(path, []byte("mode: agent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, []byte("mode: agent\nglobal:\n  hostname: renamed\n")); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "mode: agent\nglobal:\n  hostname: renamed\n" {
		t.Fatalf("content = %q err=%v", got, err)
	}
	bak, err := os.ReadFile(path + ".bak")
	if err != nil || string(bak) != "mode: agent\n" {
		t.Fatalf("backup = %q err=%v", bak, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions not preserved: %v", info.Mode())
	}
}

func TestSaveNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "monitor.yaml")
	if err := Save(path, []byte("mode: agent\n")); err != nil {
		t.Fatalf("save into missing dir/file: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "mode: agent\n" {
		t.Fatalf("content = %q err=%v", got, err)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("no backup expected for a new file, got %v", err)
	}
}

func TestCheckLocked(t *testing.T) {
	base := "mode: agent\nstream:\n  destinations: [wss://hub:20443]\n  api_key: k1\n"
	cur, err := Parse([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	same, _ := Parse([]byte(base + "global:\n  hostname: other\n"))
	if err := CheckLocked(cur, same); err != nil {
		t.Fatalf("unrelated change must pass: %v", err)
	}
	bad, _ := Parse([]byte(strings.Replace(base, "mode: agent", "mode: hub", 1)))
	if err := CheckLocked(cur, bad); err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("mode change must fail, got %v", err)
	}
	bad, _ = Parse([]byte(strings.Replace(base, "wss://hub:20443", "wss://evil:1", 1)))
	if err := CheckLocked(cur, bad); err == nil || !strings.Contains(err.Error(), "destinations") {
		t.Fatalf("destination change must fail, got %v", err)
	}
	bad, _ = Parse([]byte(strings.Replace(base, "api_key: k1", "api_key: k2", 1)))
	if err := CheckLocked(cur, bad); err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("api_key change must fail, got %v", err)
	}
}
