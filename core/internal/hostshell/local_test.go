//go:build darwin || linux || freebsd

package hostshell

import (
	"context"
	"io"
	"strings"
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
	case <-time.After(time.Second):
		t.Fatal("child not reaped")
	}
}
