//go:build !windows

package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestManagerPublishesRevisionAfterControlHandoff(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	script := writeScript(t, dir, "slow-stop.plugin", "printf 'CHART handoff.v\\nDIMENSION v\\nBEGIN handoff.v\\nSET v = 1\\nEND\\n'\nwhile :; do sleep 30; done\n")
	sink := &gatedSink{started: make(chan struct{}), release: make(chan struct{})}
	m := New(newReg(sink), []Spec{{Name: "slow-stop", Command: script}}, Options{StatePath: statePath})
	runTestManager(t, m)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(sink.release) }) }
	t.Cleanup(release)
	select {
	case <-sink.started:
	case <-time.After(10 * time.Second):
		t.Fatal("sample did not enter sink")
	}
	before := m.Catalog()
	result := make(chan error, 1)
	go func() {
		_, err := m.Apply(context.Background(), ControlRequest{Name: "slow-stop", Action: "disable", IfRevision: before.Revision})
		result <- err
	}()
	waitFor(t, func() bool {
		data, err := os.ReadFile(statePath)
		var saved controlState
		return err == nil && json.Unmarshal(data, &saved) == nil && saved.Revision == before.Revision+1
	}, "desired disable persisted")
	// The leader has exited, but the parser is still draining its sink. This
	// proves control reached the handoff, beyond the state-file fsync.
	waitFor(t, func() bool { return syscall.Kill(before.Plugins[0].PID, 0) != nil }, "old process exits while parser drains")
	during := catalogWhileDraining(t, m)
	if during.Plugins[0].Enabled && during.Revision != before.Revision {
		t.Errorf("catalog published revision %d while exposing pre-disable enabled state; previous revision %d", during.Revision, before.Revision)
	}
	release()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("disable did not finish")
	}
	after := m.Catalog()
	if after.Plugins[0].Enabled || after.Revision != before.Revision+1 {
		t.Fatalf("completed handoff: %+v", after)
	}
}

func TestManagerPublishesRescanCatalogAsOneGeneration(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	old := writeScript(t, dir, "old.plugin", "printf 'CHART rescan.v\nDIMENSION v\nBEGIN rescan.v\nSET v = 1\nEND\n'\nwhile :; do sleep 30; done\n")
	sink := &gatedSink{started: make(chan struct{}), release: make(chan struct{})}
	m := New(newReg(sink), nil, Options{Dir: dir, StatePath: statePath})
	runTestManager(t, m)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(sink.release) }) }
	t.Cleanup(release)
	select {
	case <-sink.started:
	case <-time.After(10 * time.Second):
		t.Fatal("old sample did not enter sink")
	}
	before := m.Catalog()
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	writeScript(t, dir, "next.plugin", "printf 'DISABLE\\n'\n")
	result := make(chan error, 1)
	go func() {
		_, err := m.Apply(context.Background(), ControlRequest{Action: "rescan", IfRevision: before.Revision})
		result <- err
	}()
	waitFor(t, func() bool { return syscall.Kill(before.Plugins[0].PID, 0) != nil }, "removed process exits while parser drains")
	during := catalogWhileDraining(t, m)
	if during.Revision != before.Revision || len(during.Plugins) != 1 || during.Plugins[0].Name != "old" {
		t.Errorf("rescan exposed a mixed generation: %+v", during)
	}
	release()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rescan did not finish")
	}
	after := m.Catalog()
	if after.Revision != before.Revision+1 || len(after.Plugins) != 1 || after.Plugins[0].Name != "next" {
		t.Fatalf("completed rescan: %+v", after)
	}
}

func catalogWhileDraining(t *testing.T, m *Manager) Catalog {
	t.Helper()
	result := make(chan Catalog, 1)
	go func() { result <- m.Catalog() }()
	select {
	case catalog := <-result:
		return catalog
	case <-time.After(time.Second):
		t.Fatal("catalog read blocked on worker handoff")
		return Catalog{}
	}
}
