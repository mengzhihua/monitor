//go:build !windows

package plugins

import (
	"os/exec"
	"syscall"
)

// setProcAttr puts the plugin in its own process group so cancelling the
// command also terminates children the plugin forked (e.g. shell pipelines).
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}
