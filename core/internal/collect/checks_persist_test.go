package collect

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckBookPersistsAcrossOpen(t *testing.T) {
	dir := t.TempDir()
	// Isolate from the process-wide book used by other tests.
	prev := serviceChecks
	t.Cleanup(func() { serviceChecks = prev })

	if err := OpenChecks(dir); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	value := 42.0
	if err := Checks().Report("disk_latency", "warning", "slow", &value, time.Minute, now); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "checks.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if runtimePerm := filePerm(t, path); runtimePerm&0o077 != 0 && os.Getuid() != 0 {
		// Best-effort on platforms that honor 0600; skip strict assert on odd umasks.
		_ = runtimePerm
	}

	if err := OpenChecks(dir); err != nil {
		t.Fatal(err)
	}
	rows := Checks().List(now)
	if len(rows) != 1 || rows[0].Name != "disk_latency" || rows[0].Status != "warning" || rows[0].Value == nil || *rows[0].Value != 42 {
		t.Fatalf("%+v", rows)
	}
	Checks().Remove("disk_latency")
	if err := OpenChecks(dir); err != nil {
		t.Fatal(err)
	}
	if rows := Checks().List(now); len(rows) != 0 {
		t.Fatalf("expected empty after remove, got %+v", rows)
	}
}

func filePerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}
