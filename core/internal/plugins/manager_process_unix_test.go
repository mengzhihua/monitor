//go:build !windows

package plugins

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestManagerDisableKillsTermIgnoringProcessGroup(t *testing.T) {
	dir := t.TempDir()
	childFile := filepath.Join(dir, "child.pid")
	script := writeScript(t, dir, "tree.plugin", "trap '' TERM\nsh -c 'trap \"\" TERM; while :; do sleep 30; done' &\nprintf '%s' \"$!\" > \"$2\"\nwhile :; do sleep 30; done\n")
	m := New(newReg(&memSink{}), []Spec{{Name: "tree", Command: script, Args: []string{childFile}}}, Options{})
	runTestManager(t, m)
	var child int
	waitFor(t, func() bool {
		data, err := os.ReadFile(childFile)
		if err != nil {
			return false
		}
		child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		return child > 0
	}, "descendant PID")
	parent := m.Status()[0].PID
	if err := syscall.Kill(child, 0); err != nil {
		t.Fatalf("child not alive before disable: %v", err)
	}
	control(t, m, "tree", "disable")
	waitFor(t, func() bool { return syscall.Kill(parent, 0) != nil && syscall.Kill(child, 0) != nil }, "process group reaped after disable")
}

func TestManagerNormalExitCleansOrphanedDescendant(t *testing.T) {
	dir := t.TempDir()
	childFile := filepath.Join(dir, "child.pid")
	script := writeScript(t, dir, "orphan.plugin", "sh -c 'trap \"\" TERM; while :; do sleep 30; done' >/dev/null 2>&1 &\nprintf '%s' \"$!\" > \"$2\"\nprintf 'DISABLE\\n'\n")
	m := New(newReg(&memSink{}), []Spec{{Name: "orphan", Command: script, Args: []string{childFile}}}, Options{})
	runTestManager(t, m)
	waitFor(t, func() bool { return m.Status()[0].State == StateDisabled }, "orphan plugin completed")
	data, err := os.ReadFile(childFile)
	if err != nil {
		t.Fatal(err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return syscall.Kill(child, 0) != nil }, "normal-exit descendant reaped")
}

func TestManagerDisableAllowsTermFlush(t *testing.T) {
	dir := t.TempDir()
	flushed, ready := filepath.Join(dir, "flushed"), filepath.Join(dir, "ready")
	script := writeScript(t, dir, "flush.plugin", "trap 'printf flushed > \"$2\"; exit 0' TERM\nprintf ready > \"$3\"\nwhile :; do sleep 0.05; done\n")
	m := New(newReg(&memSink{}), []Spec{{Name: "flush", Command: script, Args: []string{flushed, ready}}}, Options{})
	runTestManager(t, m)
	waitFor(t, func() bool { _, err := os.Stat(ready); return err == nil }, "signal handler ready")
	control(t, m, "flush", "disable")
	data, err := os.ReadFile(flushed)
	if err != nil || string(data) != "flushed" {
		t.Fatalf("TERM flush lost: %q %v", data, err)
	}
}
