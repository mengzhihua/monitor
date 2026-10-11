//go:build !windows

package hostshell

import pty "github.com/aymanbagabas/go-pty"

func releaseLocalPTY(p pty.Pty) error { return p.Close() }
