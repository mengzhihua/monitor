//go:build darwin

package collect

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/shirou/gopsutil/v4/process"
)

func findAppProcess(entries []appProcess, pid int32) *appProcess {
	for i := range entries {
		if entries[i].process.Pid == pid {
			return &entries[i]
		}
	}
	return nil
}

func TestAppsDarwinSnapshotIdentityAndExit(t *testing.T) {
	ctx := context.Background()
	child := exec.Command("/bin/sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	pid := int32(child.Process.Pid)
	entries, err := listAppProcesses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.process.Pid <= 0 {
			t.Fatalf("invalid PID included: %d", entry.process.Pid)
		}
	}
	sample := findAppProcess(entries, pid)
	if sample == nil {
		t.Fatal("running child omitted from snapshot")
	}
	legacy, err := process.NewProcessWithContext(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	started, err := legacy.CreateTimeWithContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sample.startedAt <= 0 || sample.startedAt/1000 != started {
		t.Fatalf("birth time mismatch: snapshot=%d existing=%d", sample.startedAt, started)
	}
	got, err := sample.process.NameWithContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want, err := legacy.NameWithContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("name changed: got %q want %q", got, want)
	}
	if _, err := sample.process.TimesWithContext(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := sample.process.MemoryInfoWithContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	entries, err = listAppProcesses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if findAppProcess(entries, pid) != nil {
		t.Fatal("exited PID retained in fresh snapshot")
	}
	if findAppProcess(entries, int32(os.Getpid())) == nil {
		t.Fatal("own PID missing")
	}
}

func TestAppsDarwinSnapshotCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := listAppProcesses(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled snapshot error = %v", err)
	}
}

func BenchmarkAppsProcessSnapshot(b *testing.B) {
	ctx := context.Background()
	for _, baseline := range []bool{true, false} {
		name := "snapshot"
		if baseline {
			name = "legacy"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if baseline {
					if _, err := process.ProcessesWithContext(ctx); err != nil {
						b.Fatal(err)
					}
				} else {
					if _, err := listAppProcesses(ctx); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}
