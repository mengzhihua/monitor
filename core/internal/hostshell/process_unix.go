//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package hostshell

import (
	"os"
	"syscall"
)

func killProcess(p *os.Process) {
	if p != nil {
		_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
		_ = p.Kill()
	}
}
