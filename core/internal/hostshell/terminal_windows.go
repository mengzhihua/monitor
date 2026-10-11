//go:build windows

package hostshell

import (
	"os"

	pty "github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/windows"
)

func prepareLocalPTY(pty.Pty) (*os.File, error) { return nil, nil }

// releaseLocalPTY is called exactly once by localSession.closePTY, serialized
// against Resize. go-pty's Cmd.Wait only reaps the process and releases its
// startup attributes; it does not close the pseudoconsole. Its Pty.Close would
// also close stdout immediately, so ownership transfers to this function after
// Cmd.Start succeeds and the wrapper never calls Pty.Close on that terminal.
func releaseLocalPTY(p pty.Pty) error {
	terminal := p.(pty.ConPty)
	// On pre-24H2 Windows this may wait for stdout to drain; force Close
	// closes the pipes before acquiring closePTY's Once to unblock it.
	// On 24H2+ this returns immediately, and stdout must stay open until EOF
	// so the console's final frame is not truncated.
	// https://learn.microsoft.com/en-us/windows/console/closepseudoconsole
	windows.ClosePseudoConsole(windows.Handle(p.Fd()))
	return terminal.InputPipe().Close()
}
