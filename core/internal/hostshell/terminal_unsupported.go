//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package hostshell

import (
	"errors"
	"os"

	pty "github.com/aymanbagabas/go-pty"
)

func prepareLocalPTY(pty.Pty) (*os.File, error) {
	return nil, errors.New("local terminal unavailable on this platform")
}
