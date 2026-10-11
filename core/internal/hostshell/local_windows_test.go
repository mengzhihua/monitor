//go:build windows

package hostshell

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestWindowsConPTYOutputAndExit(t *testing.T) {
	program, e := exec.LookPath("powershell.exe")
	if e != nil {
		t.Fatal("PowerShell required for Windows terminal acceptance")
	}
	s, e := Open(context.Background(), Target{ID: "windows", Kind: "local", Argv: []string{program, "-NoLogo", "-NoProfile", "-Command", "[Console]::WriteLine('WINDOWS_PTY_OK')"}, Dir: t.TempDir()}, Credentials{}, 80, 24)
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
	case <-time.After(time.Second):
		t.Fatal("child not reaped")
	}
}
