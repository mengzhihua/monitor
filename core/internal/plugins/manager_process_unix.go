//go:build !windows

package plugins

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Preserve a short TERM grace period for collectors that flush on shutdown,
// then kill the process group if a leader or descendant ignores the signal.
// The final cleanup owns and stops the timer before releasing the worker.
func startManagedProcess(cmd *exec.Cmd) (func(), error) {
	setProcAttr(cmd)
	var mu sync.Mutex
	var timer *time.Timer
	closed := false
	cleanup := func() {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return
		}
		closed = true
		if timer != nil {
			timer.Stop()
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.Cancel = func() error {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if timer == nil {
			timer = time.AfterFunc(time.Second, cleanup)
		}
		return err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cleanup, nil
}
