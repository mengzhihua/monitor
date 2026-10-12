//go:build windows

package hostshell

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWindowsConPTYOutputAndExit(t *testing.T) {
	program, e := exec.LookPath("powershell.exe")
	if e != nil {
		t.Fatal("PowerShell required for Windows terminal acceptance")
	}
	script := "for ($i=0; $i -lt 1024; $i++) { [Console]::WriteLine(('D'*64)) }; [Console]::WriteLine('WINDOWS_PTY_OK')"
	s, e := Open(context.Background(), Target{ID: "windows", Kind: "local", Argv: []string{program, "-NoLogo", "-NoProfile", "-Command", script}, Dir: t.TempDir()}, Credentials{}, 80, 24)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	waited := make(chan error, 1)
	go func() { waited <- s.Wait() }()
	read := make(chan string, 1)
	go func() { b, _ := io.ReadAll(s); read <- string(b) }()
	select {
	case b := <-read:
		if !strings.Contains(b, "WINDOWS_PTY_OK") {
			t.Fatal("ConPTY output absent")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ConPTY did not close")
	}
	select {
	case e := <-waited:
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

func TestWindowsConPTYCloseWithUnreadOutput(t *testing.T) {
	program, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Fatal("PowerShell required for Windows terminal acceptance")
	}
	dir := t.TempDir()
	script := "[IO.File]::WriteAllText('output-started', 'ready'); $line='X'*8192; while ($true) { [Console]::WriteLine($line) }"
	s, err := Open(context.Background(), Target{ID: "windows", Kind: "local", Argv: []string{program, "-NoLogo", "-NoProfile", "-Command", script}, Dir: dir}, Credentials{}, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the failure path bounded too: a regression must fail this test
	// instead of hanging its deferred cleanup on ClosePseudoConsole.
	defer func() { go s.Close() }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err = os.Stat(filepath.Join(dir, "output-started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("output-producing child did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Deliberately never read the terminal. Let its small pipe fill before
	// forcing shutdown, as with a disconnected or stalled browser.
	time.Sleep(200 * time.Millisecond)
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("ConPTY Close blocked on unread output")
	}
	local := s.(*localSession)
	select {
	case <-local.done:
	case <-time.After(5 * time.Second):
		t.Fatal("unread-output child was not reaped")
	}
	if err := s.Resize(90, 30); err == nil {
		t.Fatal("Resize used a closed pseudoconsole handle")
	}
}

func TestWindowsConPTYCloseReapsWithoutWait(t *testing.T) {
	program, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Fatal("PowerShell required for Windows terminal acceptance")
	}
	s, err := Open(context.Background(), Target{ID: "windows", Kind: "local", Argv: []string{program, "-NoLogo", "-NoProfile", "-Command", "Start-Sleep -Seconds 600"}, Dir: t.TempDir()}, Credentials{}, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	local := s.(*localSession)
	// Drain ConPTY output while closing; no caller starts Session.Wait.
	go io.Copy(io.Discard, s)
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-local.done:
	case <-time.After(15 * time.Second):
		t.Fatal("Close left the child unreaped")
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close deadlocked with the process waiter")
	}
	if local.cmd.ProcessState == nil {
		t.Fatal("process result absent after Close")
	}
	if first, second := s.Wait(), s.Wait(); first != local.waitErr || second != first {
		t.Fatal("Wait must preserve the original process result")
	}
}
