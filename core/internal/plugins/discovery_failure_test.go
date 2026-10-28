package plugins

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// A non-directory path fails ReadDir on every supported OS, unlike chmod which
// can still succeed under an elevated test account.
func replacePluginDirectoryWithFile(t *testing.T, dir string) func() {
	t.Helper()
	backup := dir + ".saved"
	if err := os.Rename(dir, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("temporarily unavailable"), 0o600); err != nil {
		_ = os.Rename(backup, dir)
		t.Fatal(err)
	}
	restored := false
	restore := func() {
		if restored {
			return
		}
		if err := os.Remove(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(backup, dir); err != nil {
			t.Fatal(err)
		}
		restored = true
	}
	t.Cleanup(restore)
	return restore
}

func TestRescanReadFailurePreservesCatalogAndControlState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plugins.d")
	packageFixture(t, dir, "example", nil)
	if err := os.WriteFile(filepath.Join(dir, "legacy.plugin"), []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(t.TempDir(), "state.json")
	m := New(newReg(&memSink{}), nil, Options{Dir: dir, Disabled: []string{"example", "legacy"}, StatePath: statePath})
	runTestManager(t, m)
	before := m.Catalog()
	if len(before.Plugins) != 2 {
		t.Fatalf("initial catalog = %+v", before)
	}
	if err := m.saveState(before.Revision, m.disabled); err != nil {
		t.Fatal(err)
	}
	stateBefore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	restore := replacePluginDirectoryWithFile(t, dir)
	after, err := m.Apply(context.Background(), ControlRequest{Action: "rescan", IfRevision: before.Revision})
	if err == nil {
		t.Fatalf("unreadable directory was committed as an empty catalog: %+v", after)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("failed scan changed catalog: before=%+v after=%+v", before, after)
	}
	stateAfter, err := os.ReadFile(statePath)
	if err != nil || !bytes.Equal(stateAfter, stateBefore) {
		t.Fatalf("failed scan changed durable state: %s, err=%v", stateAfter, err)
	}
	restore()
	if after := control(t, m, "", "rescan"); !reflect.DeepEqual(after, before) {
		t.Fatalf("recovered directory changed unchanged catalog: %+v", after)
	}
	if err := os.RemoveAll(filepath.Join(dir, "example")); err != nil {
		t.Fatal(err)
	}
	after = control(t, m, "", "rescan")
	if after.Revision != before.Revision+1 || len(after.Plugins) != 1 || after.Plugins[0].Name != "legacy" {
		t.Fatalf("real package removal was not applied: %+v", after)
	}
}

func TestDiscoveryRootFailureKeepsConfiguredPluginsAndDiagnostics(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(newReg(&memSink{}), []Spec{{Name: "configured", Command: "unused", Disabled: true}}, Options{Dir: dir})
	catalog := m.Catalog()
	if m.Err() != nil || len(catalog.Plugins) != 1 || catalog.Plugins[0].Name != "configured" || len(catalog.Diagnostics) != 1 {
		t.Fatalf("startup lost explicit configuration or diagnostics: %+v err=%v", catalog, m.Err())
	}
	if _, diagnostics, err := discoverManifests(context.Background(), dir); err == nil || len(diagnostics) != 1 {
		t.Fatalf("manifest discovery hid root error: diagnostics=%+v err=%v", diagnostics, err)
	}
	if _, err := discover(context.Background(), dir); err == nil {
		t.Fatal("legacy discovery hid root error")
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, diagnostics, err := discoverManifests(context.Background(), missing); err != nil || len(diagnostics) != 0 {
		t.Fatalf("optional missing directory rejected: %+v %v", diagnostics, err)
	}
}

func TestRescanReadFailureKeepsRunningWorker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell process fixture")
	}
	dir := filepath.Join(t.TempDir(), "plugins.d")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeScript(t, dir, "worker.plugin", "printf 'CHART continuity.v\\nDIMENSION v\\n'\nwhile :; do printf 'BEGIN continuity.v\\nSET v = 1\\nEND\\n'; sleep 0.1; done\n")
	sink := &memSink{}
	m := New(newReg(sink), nil, Options{Dir: dir})
	runTestManager(t, m)
	count := func() int {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		return len(sink.rows["continuity.v|v"])
	}
	waitFor(t, func() bool { return count() > 0 }, "first plugin sample")
	pid, revision := statusNamed(m, "worker").PID, m.Catalog().Revision
	replacePluginDirectoryWithFile(t, dir)
	if _, err := m.Apply(context.Background(), ControlRequest{Action: "rescan", IfRevision: revision}); err == nil {
		t.Fatal("unreadable directory scan succeeded")
	}
	previous := count()
	waitFor(t, func() bool { return count() > previous }, "continued sampling after failed scan")
	status := statusNamed(m, "worker")
	if status.PID != pid || status.State != StateRunning || m.Catalog().Revision != revision {
		t.Fatalf("failed scan replaced the running generation: %+v", status)
	}
}
