//go:build darwin || linux || freebsd

package hostshell

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
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

func TestLocalPTYCloseInterruptsBlockedInput(t *testing.T) {
	s, err := Open(context.Background(), Target{ID: "blocked-input", Kind: "local", Argv: []string{"/bin/sh", "-c", "stty raw -echo; printf 'INPUT_BLOCK_READY'; exec sleep 600"}, Dir: t.TempDir()}, Credentials{}, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	// Keep regressions bounded even if Close itself stops being cancellable.
	defer func() { go s.Close() }()
	local := s.(*localSession)
	if local.master == nil {
		t.Fatal("Unix terminal has no pollable master")
	}
	control, err := local.master.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var flags int
	var flagErr error
	if err = control.Control(func(fd uintptr) { flags, flagErr = unix.FcntlInt(fd, unix.F_GETFL, 0) }); err != nil || flagErr != nil || flags&unix.O_NONBLOCK == 0 {
		t.Fatalf("PTY must stay nonblocking: flags=%d err=%v control=%v", flags, flagErr, err)
	}
	if err = local.master.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	buf := make([]byte, 256)
	for !strings.Contains(output.String(), "INPUT_BLOCK_READY") {
		n, err := s.Read(buf)
		output.Write(buf[:n])
		if err != nil {
			t.Fatalf("raw terminal did not become ready: %v", err)
		}
	}
	if err = local.master.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	// The child never reads stdin. A megabyte must fill its small input queue;
	// a real deadline first proves this descriptor uses the Go runtime poller.
	input := bytes.Repeat([]byte("x"), 1<<20)
	if err = local.master.SetWriteDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Write(input); !errors.Is(err, os.ErrDeadlineExceeded) || n == len(input) {
		t.Fatalf("full PTY input did not respect its deadline: n=%d err=%v", n, err)
	}
	if err = local.master.SetWriteDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	wrote := make(chan error, 1)
	go func() { _, err := s.Write(input); wrote <- err }()
	select {
	case err := <-wrote:
		t.Fatalf("expected blocked PTY input before Close, got %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked on full PTY input")
	}
	select {
	case err := <-wrote:
		if err == nil {
			t.Fatal("blocked Write unexpectedly succeeded after Close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not wake blocked PTY Write")
	}
	select {
	case <-local.done:
	case <-time.After(5 * time.Second):
		t.Fatal("blocked-input child was not reaped")
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
