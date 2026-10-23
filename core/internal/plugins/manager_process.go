package plugins

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func (m *Manager) set(p *plugin, st State, err string) {
	p.mu.Lock()
	p.state, p.err = st, redactProcessText(p.spec, err)
	if st == StateDisabled {
		p.enabled = false
	}
	if st != StateRunning {
		p.pid = 0
	}
	p.mu.Unlock()
}

func (m *Manager) supervise(ctx context.Context, p *plugin) {
	delay := m.opt.RestartMin
	for {
		start := time.Now()
		err := m.runOnce(ctx, p)
		if ctx.Err() != nil {
			m.set(p, StateStopped, "")
			return
		}
		switch {
		case errors.Is(err, ErrDisabled):
			m.set(p, StateDisabled, "plugin sent DISABLE")
			m.log.Info("plugin disabled itself", "plugin", p.spec.Name)
			return
		case errors.Is(err, errNotStartable):
			m.set(p, StateFailed, err.Error())
			m.log.Warn("plugin cannot start", "plugin", p.spec.Name, "err", redactProcessText(p.spec, err.Error()))
			return
		}
		msg := "exited"
		if err != nil {
			msg = redactProcessText(p.spec, err.Error())
		}
		// a plugin that survived long enough earns a fresh back-off
		if time.Since(start) > 5*m.opt.RestartMax {
			delay = m.opt.RestartMin
		}
		m.set(p, StateWaiting, msg)
		m.log.Warn("plugin exited, restarting", "plugin", p.spec.Name, "reason", msg, "in", delay)
		select {
		case <-ctx.Done():
			m.set(p, StateStopped, "")
			return
		case <-time.After(delay):
		}
		p.mu.Lock()
		p.restart++
		p.mu.Unlock()
		delay = min(delay*2, m.opt.RestartMax)
	}
}

var errNotStartable = errors.New("not startable")

// runOnce runs the process until it exits, the watchdog fires or ctx ends.
func (m *Manager) runOnce(ctx context.Context, p *plugin) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Parser state is process-local; collected Registry history survives restarts.
	parser := newPluginParser(p.spec, m.reg)
	p.mu.Lock()
	p.parser = parser
	p.mu.Unlock()
	if _, err := os.Stat(p.spec.Command); err != nil {
		return fmt.Errorf("%w: %v", errNotStartable, err)
	}
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	args := append([]string{strconv.Itoa(p.spec.UpdateEvery)}, p.spec.Args...)
	cmd := exec.CommandContext(pctx, p.spec.Command, args...)
	cmd.Env = append(pluginEnvironment(p.spec),
		"MONITOR_UPDATE_EVERY="+strconv.Itoa(p.spec.UpdateEvery),
		"MONITOR_HOSTNAME="+m.reg.Host.Hostname,
		"MONITOR_PLUGIN="+p.spec.Name,
		"NETDATA_UPDATE_EVERY="+strconv.Itoa(p.spec.UpdateEvery), // many existing plugins read this
	)
	cmd.Env = append(cmd.Env, p.spec.Env...)
	// Let exec own the OS-pipe copying goroutines. StdoutPipe/StderrPipe
	// cannot be read concurrently with Wait: Wait closes their read ends as
	// soon as the process exits, potentially discarding buffered protocol data.
	stdout, outWriter := io.Pipe()
	stderr, errWriter := io.Pipe()
	defer stdout.Close()
	defer stderr.Close()
	defer outWriter.Close()
	defer errWriter.Close()
	cmd.Stdout, cmd.Stderr = outWriter, errWriter
	cmd.Stdin = nil
	cmd.WaitDelay = 3 * time.Second // then force-close pipes held open by orphaned children
	cleanup, startErr := startManagedProcess(cmd)
	if err := startErr; err != nil {
		var pe *os.PathError
		if errors.As(err, &pe) || errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("%w: %v", errNotStartable, err)
		}
		return err
	}
	defer cleanup()
	p.mu.Lock()
	p.state, p.pid, p.started, p.err = StateRunning, cmd.Process.Pid, time.Now(), ""
	p.mu.Unlock()
	m.log.Info("plugin started", "plugin", p.spec.Name, "pid", cmd.Process.Pid, "cmd", p.spec.Command)

	// Join stderr draining before releasing worker ownership.
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		defer stderr.Close()
		m.drainStderr(p, stderr)
	}()

	// watchdog: kill when no output for Timeout seconds
	timeout := time.Duration(p.spec.Timeout) * time.Second
	wdDone := make(chan struct{})
	var wdErr error
	go func() {
		defer close(wdDone)
		t := time.NewTicker(timeout / 4)
		defer t.Stop()
		for {
			select {
			case <-pctx.Done():
				return
			case <-t.C:
				if time.Since(parser.lastActivity()) > timeout {
					wdErr = fmt.Errorf("no output for %s", timeout)
					cancel()
					return
				}
			}
		}
	}()

	// Wait drains the exec copying goroutines before we signal EOF to the
	// parser. WaitDelay still bounds OS pipes held open by orphaned children.
	waitc := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		cleanup()
		_ = outWriter.Close()
		_ = errWriter.Close()
		waitc <- err
	}()

	parser.touch()
	perr := parser.Run(stdout)
	_ = stdout.Close() // DISABLE/EXIT must also unblock an active copying writer.
	cancel()
	<-wdDone
	werr := <-waitc
	<-stderrDone
	switch {
	case errors.Is(perr, ErrDisabled), errors.Is(perr, ErrExit):
		return perr
	case wdErr != nil:
		return wdErr
	case ctx.Err() != nil:
		return nil
	case werr != nil && !errors.Is(werr, context.Canceled):
		return werr
	case perr != nil && !errors.Is(perr, os.ErrClosed):
		return perr
	}
	return nil
}

// Manifest packages receive only the operating system runtime environment.
// Configured and legacy collectors retain inherited environments for compatibility.
func pluginEnvironment(sp Spec) []string {
	if sp.Manifest == nil {
		return os.Environ()
	}
	allowed := map[string]bool{"PATH": true, "LANG": true, "LC_ALL": true, "LC_CTYPE": true, "TMPDIR": true, "TMP": true, "TEMP": true, "SYSTEMROOT": true, "WINDIR": true, "PATHEXT": true}
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if allowed[strings.ToUpper(key)] {
			env = append(env, entry)
		}
	}
	return env
}
