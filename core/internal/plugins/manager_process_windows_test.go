//go:build windows

package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// The test binary doubles as a real collector. TestMain runs before flag
// parsing, so the plugin protocol's first interval argument remains supported.
func TestMain(m *testing.M) {
	switch os.Getenv("MONITOR_TEST_PLUGIN_HELPER") {
	case "leader":
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "MONITOR_TEST_PLUGIN_HELPER=child")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		data, _ := json.Marshal(windowsPluginPIDs{Parent: os.Getpid(), Child: child.Process.Pid})
		if err := os.WriteFile(os.Getenv("MONITOR_TEST_PLUGIN_PIDS"), data, 0o600); err != nil {
			os.Exit(3)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "child":
		for {
			time.Sleep(time.Hour)
		}
	case "oneshot":
		fmt.Print("CHART windows.v\nDIMENSION v\nBEGIN windows.v\nSET v = 7\nEND\nDISABLE\n")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type windowsPluginPIDs struct{ Parent, Child int }

func TestManagerWindowsOwnsImmediateDescendants(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "pids.json")
	m := New(newReg(&memSink{}), []Spec{{Name: "tree", Command: executable, Env: []string{"MONITOR_TEST_PLUGIN_HELPER=leader", "MONITOR_TEST_PLUGIN_PIDS=" + pidFile}}}, Options{})
	runTestManager(t, m)
	previous := 0
	for _, action := range []string{"restart", "restart", "disable"} {
		var pids windowsPluginPIDs
		waitFor(t, func() bool {
			data, err := os.ReadFile(pidFile)
			return err == nil && json.Unmarshal(data, &pids) == nil && pids.Parent != previous && pids.Parent > 0 && pids.Child > 0
		}, "helper parent and immediate descendant")
		previous = pids.Parent
		handles := []windows.Handle{}
		for _, pid := range []int{pids.Parent, pids.Child} {
			handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
			if err != nil {
				t.Fatal(err)
			}
			handles = append(handles, handle)
			t.Cleanup(func() { windows.CloseHandle(handle) })
		}
		control(t, m, "tree", action)
		for _, handle := range handles {
			state, err := windows.WaitForSingleObject(handle, 5000)
			if err != nil || state != windows.WAIT_OBJECT_0 {
				t.Fatalf("%s left process running: state=%d err=%v", action, state, err)
			}
		}
	}
	if st := m.Status()[0]; st.PID != 0 || st.Enabled || st.State != StateDisabled {
		t.Fatalf("disabled tree: %+v", st)
	}
}

func TestManagerWindowsDrainsImmediateExit(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sink := &memSink{}
	m := New(newReg(sink), []Spec{{Name: "oneshot", Command: executable, Env: []string{"MONITOR_TEST_PLUGIN_HELPER=oneshot"}}}, Options{})
	runTestManager(t, m)
	waitFor(t, func() bool { return m.Status()[0].State == StateDisabled }, "immediate collector output drained")
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if rows := sink.rows["windows.v|v"]; len(rows) != 1 || rows[0] != 7 {
		t.Fatalf("one-shot samples = %v", rows)
	}
}
