package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func runTestManager(t *testing.T, m *Manager) func() {
	t.Helper()
	if err := m.Err(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("manager shutdown timed out")
			}
		})
	}
	t.Cleanup(stop)
	waitFor(t, func() bool { return m.Catalog().Running }, "control plane ready")
	return stop
}

func statusNamed(m *Manager, name string) Status {
	for _, status := range m.Status() {
		if status.Name == name {
			return status
		}
	}
	return Status{}
}

func control(t *testing.T, m *Manager, name, action string) Catalog {
	t.Helper()
	catalog, err := m.Apply(context.Background(), ControlRequest{Name: name, Action: action, IfRevision: m.Catalog().Revision})
	if err != nil {
		t.Fatalf("%s %s: %v", action, name, err)
	}
	return catalog
}

func TestManagerEmptyCatalogRemainsControllable(t *testing.T) {
	m := New(newReg(&memSink{}), nil, Options{})
	stop := runTestManager(t, m)
	before := m.Catalog()
	after := control(t, m, "", "rescan")
	if after.Revision != before.Revision || len(after.Plugins) != 0 || !after.Running {
		t.Fatalf("empty catalog: %+v", after)
	}
	stop()
	if m.Catalog().Running {
		t.Fatal("stopped manager is still running")
	}
	if _, err := m.Apply(context.Background(), ControlRequest{Action: "rescan", IfRevision: after.Revision}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("stopped Apply = %v", err)
	}
}

func TestManagerControlsPersistAndPreserveHistory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell processes")
	}
	dir := t.TempDir()
	script := writeScript(t, dir, "worker.plugin", "printf 'CHART lifecycle.v\nDIMENSION v\nBEGIN lifecycle.v\nSET v = 9\nEND\n'\nwhile :; do sleep 30; done\n")
	statePath := filepath.Join(t.TempDir(), "state.json")
	sink := &memSink{}
	specs := []Spec{{Name: "worker", Command: script}, {Name: "locked", Command: script, Disabled: true}}
	m := New(newReg(sink), specs, Options{StatePath: statePath})
	runTestManager(t, m)
	waitFor(t, func() bool { return statusNamed(m, "worker").State == StateRunning }, "worker running")
	waitFor(t, func() bool { sink.mu.Lock(); defer sink.mu.Unlock(); return len(sink.rows["lifecycle.v|v"]) >= 1 }, "initial sample")
	initial := m.Catalog()
	for _, request := range []struct {
		req  ControlRequest
		want error
	}{
		{ControlRequest{Name: "worker", Action: "disable", IfRevision: 0}, ErrConflict},
		{ControlRequest{Name: "missing", Action: "disable", IfRevision: initial.Revision}, ErrNotFound},
		{ControlRequest{Name: "locked", Action: "enable", IfRevision: initial.Revision}, ErrPolicy},
		{ControlRequest{Name: "worker", Action: "bad", IfRevision: initial.Revision}, ErrInvalid},
		{ControlRequest{Name: "worker", Action: "rescan", IfRevision: initial.Revision}, ErrInvalid},
	} {
		if _, err := m.Apply(context.Background(), request.req); !errors.Is(err, request.want) {
			t.Fatalf("%+v = %v, want %v", request.req, err, request.want)
		}
	}
	disabled := control(t, m, "worker", "disable")
	if disabled.Revision != initial.Revision+1 || statusNamed(m, "worker").State != StateDisabled {
		t.Fatalf("disable result: %+v", disabled)
	}
	persisted := New(newReg(&memSink{}), specs, Options{StatePath: statePath})
	if persisted.Err() != nil || persisted.Catalog().Revision != disabled.Revision || statusNamed(persisted, "worker").Enabled {
		t.Fatalf("persisted disable = %+v err=%v", persisted.Catalog(), persisted.Err())
	}
	info, err := os.Stat(statePath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state permissions = %v %v", info, err)
	}
	control(t, m, "worker", "enable")
	waitFor(t, func() bool { return statusNamed(m, "worker").State == StateRunning }, "enabled worker")
	waitFor(t, func() bool { sink.mu.Lock(); defer sink.mu.Unlock(); return len(sink.rows["lifecycle.v|v"]) >= 2 }, "sample after enabling")
	pid := statusNamed(m, "worker").PID
	control(t, m, "worker", "restart")
	waitFor(t, func() bool { st := statusNamed(m, "worker"); return st.State == StateRunning && st.PID != pid }, "new worker generation")
	waitFor(t, func() bool { sink.mu.Lock(); defer sink.mu.Unlock(); return len(sink.rows["lifecycle.v|v"]) >= 3 }, "samples preserved across disable and restart")
	revision := m.Catalog().Revision
	time.Sleep(30 * time.Millisecond)
	if m.Catalog().Revision != revision {
		t.Fatal("sampling changed management revision")
	}
	control(t, m, "worker", "disable")
	if !m.Catalog().Running {
		t.Fatal("no enabled workers stopped control plane")
	}
}

func TestManagerStateLoadFailsClosed(t *testing.T) {
	for _, contents := range []string{"broken", `null`, `{"schema_version":2,"revision":1,"disabled":{}}`, `{"schema_version":1,"revision":0,"disabled":{}}`, `{"schema_version":1,"revision":1}`, `{"schema_version":1,"revision":1,"disabled":{},"token":"secret"}`, `{"schema_version":1,"revision":1,"disabled":{}} {}`} {
		t.Run(fmt.Sprintf("case-%d", len(contents)), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			m := New(newReg(&memSink{}), []Spec{{Name: "never", Command: "never-execute"}}, Options{StatePath: path})
			if m.Err() == nil {
				t.Fatal("invalid state accepted")
			}
			m.Run(context.Background())
			if m.Catalog().Running || m.Status()[0].Enabled {
				t.Fatalf("bad state allowed execution: %+v", m.Catalog())
			}
			if strings.Contains(m.Err().Error(), "secret") {
				t.Fatal("state content leaked through error")
			}
		})
	}
}

func TestManagerStateWriteFailureLeavesWorkerUnchanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell processes")
	}
	dir := t.TempDir()
	path := writeScript(t, dir, "stable.plugin", "while :; do sleep 30; done\n")
	statePath := filepath.Join(dir, "state.json")
	m := New(newReg(&memSink{}), []Spec{{Name: "stable", Command: path}}, Options{StatePath: statePath})
	runTestManager(t, m)
	waitFor(t, func() bool { return m.Status()[0].State == StateRunning }, "stable process")
	before := m.Catalog()
	if err := os.Mkdir(statePath, 0o700); err != nil {
		t.Fatal(err)
	}
	after, err := m.Apply(context.Background(), ControlRequest{Name: "stable", Action: "disable", IfRevision: before.Revision})
	if !errors.Is(err, ErrUnavailable) || after.Revision != before.Revision || after.Plugins[0].PID != before.Plugins[0].PID || !after.Plugins[0].Enabled {
		t.Fatalf("failed save changed worker: before=%+v after=%+v error=%v", before, after, err)
	}
}

func TestManagerConcurrentControlsRejectStaleWriter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell processes")
	}
	path := writeScript(t, t.TempDir(), "race.plugin", "while :; do sleep 30; done\n")
	m := New(newReg(&memSink{}), []Spec{{Name: "race", Command: path}}, Options{})
	runTestManager(t, m)
	waitFor(t, func() bool { return m.Status()[0].State == StateRunning }, "race worker")
	revision := m.Catalog().Revision
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 200 {
				_ = m.Catalog()
				_ = m.Status()
			}
		})
	}
	results := make(chan error, 2)
	for _, action := range []string{"disable", "restart"} {
		wg.Go(func() {
			_, err := m.Apply(context.Background(), ControlRequest{Name: "race", Action: action, IfRevision: revision})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflict)
	}
	if err := m.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := m.Apply(ctx, ControlRequest{Name: "race", Action: "disable", IfRevision: m.Catalog().Revision})
	m.release()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued cancellation: %v", err)
	}
}

func writeManagerManifest(t *testing.T, dir, id, body string) string {
	t.Helper()
	packageDir := filepath.Join(dir, id)
	if err := os.MkdirAll(packageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	command := writeScript(t, packageDir, "run", body)
	manifest := fmt.Sprintf(`{"api_version":1,"id":%q,"name":%q,"version":"1.0","protocol":"plugins.d/v1","capabilities":["metrics"],"command":"run"}`, id, id)
	if err := os.WriteFile(filepath.Join(packageDir, "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return command
}

func TestManagerRescanPreservesUnchangedAndIsolatesInvalid(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell processes")
	}
	dir := t.TempDir()
	body := "while :; do sleep 30; done\n"
	stable := writeScript(t, dir, "stable.plugin", body)
	changed := writeScript(t, dir, "changed.plugin", body)
	writeScript(t, dir, "package.plugin", body)
	writeManagerManifest(t, dir, "package", body)
	writeScript(t, dir, "broken.plugin", body)
	if err := os.Mkdir(filepath.Join(dir, "broken"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken", "plugin.json"), []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(newReg(&memSink{}), nil, Options{Dir: dir})
	runTestManager(t, m)
	waitFor(t, func() bool {
		return statusNamed(m, "stable").PID > 0 && statusNamed(m, "changed").PID > 0 && statusNamed(m, "package").PID > 0
	}, "discovered workers")
	before := m.Catalog()
	if statusNamed(m, "broken").Name != "" || len(before.Diagnostics) != 1 || statusNamed(m, "package").Source != "manifest" {
		t.Fatalf("bad package fallback or precedence: %+v", before)
	}
	stablePID, changedPID := statusNamed(m, "stable").PID, statusNamed(m, "changed").PID
	if after := control(t, m, "", "rescan"); after.Revision != before.Revision {
		t.Fatalf("unchanged rescan revised catalog: %+v", after)
	}
	writeScript(t, dir, filepath.Base(changed), "# changed same-path executable\n"+body)
	after := control(t, m, "", "rescan")
	waitFor(t, func() bool { return statusNamed(m, "changed").PID > 0 && statusNamed(m, "changed").PID != changedPID }, "changed binary restarted")
	if after.Revision != before.Revision+1 || statusNamed(m, "stable").PID != stablePID {
		t.Fatal("rescan restarted an unchanged worker")
	}
	if err := os.Remove(stable); err != nil {
		t.Fatal(err)
	}
	control(t, m, "", "rescan")
	if statusNamed(m, "stable").Name != "" {
		t.Fatal("deleted legacy plugin remains")
	}
	// Turning a running package invalid stops it without falling back to legacy.
	if err := os.WriteFile(filepath.Join(dir, "package", "plugin.json"), []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	control(t, m, "", "rescan")
	if statusNamed(m, "package").Name != "" || len(m.Catalog().Diagnostics) != 2 {
		t.Fatalf("invalid package still running: %+v", m.Catalog())
	}
}

func TestManagerManifestEnvironmentAndPublicMetadata(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell processes")
	}
	t.Setenv("MONITOR_MANAGER_TEST_SECRET", "never-inherit-this-secret")
	dir := t.TempDir()
	writeManagerManifest(t, dir, "private", "if [ -n \"$MONITOR_MANAGER_TEST_SECRET\" ]; then printf 'VARIABLE leaked = 1\\n'; fi\nprintf 'VARIABLE interval = %s\\n' \"$MONITOR_UPDATE_EVERY\"\nprintf 'DISABLE\\n'\n")
	m := New(newReg(&memSink{}), nil, Options{Dir: dir})
	runTestManager(t, m)
	waitFor(t, func() bool { return m.Status()[0].State == StateDisabled }, "manifest completes")
	status := m.Status()[0]
	if status.Variables["leaked"] != 0 || status.Variables["interval"] != 1 {
		t.Fatalf("manifest environment = %v", status.Variables)
	}
	status.Manifest.Capabilities[0] = "mutated"
	if m.Status()[0].Manifest.Capabilities[0] != "metrics" {
		t.Fatal("public metadata aliases manager storage")
	}
	if status.Manifest.Command != "" || len(status.Manifest.Args) > 0 {
		t.Fatal("execution fields in public metadata")
	}
	data, err := json.Marshal(m.Catalog())
	if err != nil || bytes.Contains(data, []byte("never-inherit-this-secret")) {
		t.Fatalf("catalog secret leak: %v", err)
	}
}

func TestManagerLogsRedactExplicitExecutionValues(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell processes")
	}
	path := writeScript(t, t.TempDir(), "private.plugin", "printf '%s %s\\n' \"$PRIVATE_KEY\" \"$2\" >&2\nprintf 'DISABLE\\n'\n")
	var output bytes.Buffer
	m := New(newReg(&memSink{}), []Spec{{Name: "private", Command: path, Args: []string{"secret-argument"}, Env: []string{"PRIVATE_KEY=secret-environment"}}}, Options{Logger: slog.New(slog.NewTextHandler(&output, nil))})
	stop := runTestManager(t, m)
	waitFor(t, func() bool { return m.Status()[0].State == StateDisabled }, "private process completes")
	stop()
	for _, secret := range []string{"secret-argument", "secret-environment"} {
		if strings.Contains(output.String(), secret) {
			t.Fatal("execution secret leaked to logs")
		}
	}
}
