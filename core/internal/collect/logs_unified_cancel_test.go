package collect

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func TestUnifiedCommandCancelsWithInheritedStdout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell fixture")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The background child really inherits stdout. Its PID is emitted only
	// after it starts, so cancellation cannot race ahead of creating the holder.
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `sleep 30 & printf '{"eventMessage":"%s"}\n' "$!"; exec sleep 30`)
	holderPID := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- runUnifiedStream(ctx, cmd, func() {}, func(row LogRow) { holderPID <- row.Message })
	}()
	var pid int
	select {
	case text := <-holderPID:
		var err error
		pid, err = strconv.Atoi(text)
		if err != nil || pid <= 0 {
			t.Fatalf("invalid pipe-holder PID %q", text)
		}
	case err := <-done:
		t.Fatalf("stream exited before the pipe holder started: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("pipe holder did not start")
	}
	holder, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = holder.Kill()
		_ = holder.Release()
	}()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled stream returned no error")
		}
	case <-time.After(3 * time.Second):
		// Release the inherited pipe even on a regression, then reap the
		// command and let its reader finish before the test exits.
		_ = holder.Kill()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("stream did not finish after releasing the inherited pipe")
		}
		t.Fatal("cancellation waited for a grandchild holding stdout")
	}
}
