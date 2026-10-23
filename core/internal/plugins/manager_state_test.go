package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagerRejectsStateItCannotReload(t *testing.T) {
	for _, test := range []struct {
		name       string
		pluginName string
		disabled   map[string]bool
	}{
		{name: "name exceeds reader limit", pluginName: strings.Repeat("x", 1025), disabled: map[string]bool{"retired": true}},
		{name: "invalid UTF-8 name expands during JSON encoding", pluginName: strings.Repeat("\xff", 400), disabled: map[string]bool{"retired": true}},
		{name: "encoded state exceeds reader limit", pluginName: strings.Repeat("<", 1024), disabled: nearLimitDisabledState()},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			previous, err := json.Marshal(controlState{SchemaVersion: 1, Revision: 7, Disabled: test.disabled})
			if err != nil {
				t.Fatal(err)
			}
			if len(previous) > 1<<20 {
				t.Fatal("initial state must fit the reader limit")
			}
			if err := os.WriteFile(path, previous, 0o600); err != nil {
				t.Fatal(err)
			}
			specs := []Spec{{Name: test.pluginName, Command: filepath.Join(t.TempDir(), "missing")}}
			options := Options{StatePath: path, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			m := New(newReg(&memSink{}), specs, options)
			runTestManager(t, m)
			waitFor(t, func() bool { return m.Status()[0].State == StateFailed }, "missing worker failure")
			before := m.Catalog()
			after, err := m.Apply(context.Background(), ControlRequest{Name: test.pluginName, Action: "disable", IfRevision: before.Revision})
			if err == nil {
				restarted := New(newReg(&memSink{}), specs, options)
				t.Fatalf("disable succeeded but its persisted state cannot be safely reused: reload error=%v", restarted.Err())
			}
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("unsafe state error=%v, want ErrUnavailable", err)
			}
			if after.Revision != before.Revision || !after.Plugins[0].Enabled || after.Plugins[0].State != before.Plugins[0].State {
				t.Fatalf("rejected save changed catalog: revision=%d state=%s", after.Revision, after.Plugins[0].State)
			}
			actual, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(actual, previous) {
				t.Fatalf("rejected save replaced the last reloadable state: %v", readErr)
			}
			if restarted := New(newReg(&memSink{}), specs, options); restarted.Err() != nil || restarted.Catalog().Revision != before.Revision {
				t.Fatalf("original state no longer reloads: %v", restarted.Err())
			}
		})
	}
}

func nearLimitDisabledState() map[string]bool {
	disabled := make(map[string]bool, 1011)
	for i := range 1011 {
		disabled[fmt.Sprintf("retired-%04d-", i)+strings.Repeat("a", 1011)] = true
	}
	return disabled
}

func TestManagerPersistsReaderBoundaryName(t *testing.T) {
	name := strings.Repeat("x", 1024)
	specs := []Spec{{Name: name, Command: filepath.Join(t.TempDir(), "missing")}}
	options := Options{StatePath: filepath.Join(t.TempDir(), "state.json"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	m := New(newReg(&memSink{}), specs, options)
	runTestManager(t, m)
	disabled := control(t, m, name, "disable")
	restarted := New(newReg(&memSink{}), specs, options)
	if restarted.Err() != nil || restarted.Catalog().Revision != disabled.Revision || restarted.Status()[0].Enabled {
		t.Fatalf("valid boundary state did not round-trip: %v", restarted.Err())
	}
}
