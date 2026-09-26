package collect

import (
	"context"
	"io"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestExecRunTimesOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sleep fixture")
	}
	run := execRun(100 * time.Millisecond)
	start := time.Now()
	_, err := run(context.Background(), "/bin/sleep", "30")
	if err == nil {
		t.Fatal("expected an error from the killed command")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("timeout took %v, want well under 2s", el)
	}
}

// TestExecRunReturnsAfterKillWithPipeHolders reproduces the wedged-collector
// shape: the context deadline SIGKILLs the child, but a grandchild still
// holds the output pipe, so Output's internal wait must not block until the
// grandchild exits — WaitDelay has to cut it short.
func TestExecRunReturnsAfterKillWithPipeHolders(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell fixture")
	}
	run := execRun(100 * time.Millisecond)
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := run(context.Background(), "/bin/sh", "-c", `sleep 30 & exec sleep 30`)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error after the child was killed")
		}
		if el := time.Since(start); el > 3*time.Second {
			t.Fatalf("returned after %v, want timeout+WaitDelay <3s", el)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("execRun never returned: killed child left the pipe held by a grandchild")
	}
}

func TestWaitCommandClosesPipeHeldByGrandchild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell fixture")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `sleep 30 >/dev/null & exec sleep 30`)
	cmd.WaitDelay = execWaitDelay
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
		done <- waitCommand(ctx, cmd, func() { _, _ = io.Copy(io.Discard, stdout) })
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error after cancellation")
		}
		if el := time.Since(start); el > 5*time.Second {
			t.Fatalf("returned after %v", el)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("waitCommand never returned while a grandchild held the pipe")
	}
}
