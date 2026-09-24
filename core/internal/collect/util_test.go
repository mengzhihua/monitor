package collect

import (
	"context"
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
