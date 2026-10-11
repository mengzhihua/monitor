//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package hostshell

import "os"

func killProcess(p *os.Process) {
	if p != nil {
		_ = p.Kill()
	}
}
