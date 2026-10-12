//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package hostshell

import (
	"errors"
	"os"
	"time"

	pty "github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/unix"
)

// prepareLocalPTY gives the session a pollable master with independent ownership.
// creack/pty's initialization calls File.Fd, which restores blocking mode on
// Linux; on macOS/FreeBSD its initial os.NewFile is not pollable at all. Merely
// restoring O_NONBLOCK is therefore not enough on every supported Unix host.
//
// Duplicate through Control (never Fd), then wrap the nonblocking duplicate so
// os.NewFile registers it with Go's poller. The original remains owned by pty
// for Resize and cleanup; the duplicate is owned exclusively by localSession.
func prepareLocalPTY(p pty.Pty) (*os.File, error) {
	terminal, ok := p.(pty.UnixPty)
	if !ok {
		return nil, errors.New("Unix terminal required")
	}
	fd := -1
	var controlErr error
	if err := terminal.Control(func(original uintptr) {
		if controlErr = unix.SetNonblock(int(original), true); controlErr != nil {
			return
		}
		fd, controlErr = unix.FcntlInt(original, unix.F_DUPFD_CLOEXEC, 0)
	}); err != nil {
		return nil, err
	}
	if controlErr != nil {
		return nil, controlErr
	}
	master := os.NewFile(uintptr(fd), terminal.Master().Name())
	if master == nil {
		_ = unix.Close(fd)
		return nil, errors.New("invalid terminal descriptor")
	}
	// Fail closed before starting a child if epoll/kqueue registration failed.
	// Successful deadlines establish that blocked I/O can be evicted by Close.
	if err := master.SetReadDeadline(time.Time{}); err != nil {
		master.Close()
		return nil, err
	}
	if err := master.SetWriteDeadline(time.Time{}); err != nil {
		master.Close()
		return nil, err
	}
	return master, nil
}
