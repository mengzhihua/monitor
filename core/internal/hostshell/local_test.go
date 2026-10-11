//go:build darwin || linux || freebsd

package hostshell

import (
	"context"
	"io"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLocalPTYResizeExitAndEnvironment(t *testing.T) {
	t.Setenv("MONITOR_TEST_PRIVATE_SECRET", "must-not-inherit")
	s, e := Open(context.Background(), Target{ID: "local", Kind: "local", Argv: []string{"/bin/sh"}, Dir: t.TempDir()}, Credentials{}, 80, 24)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	done := make(chan error, 1)
	go func() { done <- s.Wait() }()
	if e = s.Resize(99, 37); e != nil {
		t.Fatal(e)
	}
	var b []byte
	read := make(chan struct{})
	go func() { b, _ = io.ReadAll(s); close(read) }()
	s.Write([]byte("stty size; printf '__LOCAL_%s__\\n' \"${MONITOR_TEST_PRIVATE_SECRET-unset}\"; exit\n"))
	select {
	case <-read:
	case <-time.After(5 * time.Second):
		t.Fatal("PTY output did not close")
	}
	if !strings.Contains(string(b), "37 99") || !strings.Contains(string(b), "__LOCAL_unset__") {
		t.Fatal("PTY resize or environment isolation failed")
	}
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
		if e = s.Wait(); e != nil {
			t.Fatalf("repeated Wait changed the result: %v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("child not reaped")
	}
}

func TestLocalCloseReapsWithoutWait(t *testing.T) {
	s, err := Open(context.Background(), Target{ID: "local", Kind: "local", Argv: []string{"/bin/sh", "-c", "exec sleep 600"}, Dir: t.TempDir()}, Credentials{}, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	local := s.(*localSession)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	// Do not call Session.Wait: a failed WebSocket ready write has no waiter.
	select {
	case <-local.done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close left the child unreaped")
	}
	if local.cmd.ProcessState == nil || local.waitErr == nil {
		t.Fatal("expected the killed process to have been reaped")
	}
	var status syscall.WaitStatus
	if _, err = syscall.Wait4(local.cmd.Process.Pid, &status, syscall.WNOHANG, nil); err != syscall.ECHILD {
		t.Fatalf("process is still waitable after Close: %v", err)
	}
	if first, second := s.Wait(), s.Wait(); first != local.waitErr || second != first {
		t.Fatal("Wait must preserve the original process result")
	}
}
