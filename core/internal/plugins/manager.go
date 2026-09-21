package plugins

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// Spec describes one external plugin process.
type Spec struct {
	Name        string   `yaml:"name"`
	Command     string   `yaml:"command"` // executable path (relative paths resolve against Options.Dir)
	Args        []string `yaml:"args"`
	UpdateEvery int      `yaml:"update_every"` // passed as first argument; default host update_every
	Timeout     int      `yaml:"timeout"`      // seconds without output before the plugin is killed (0 = 10×update_every, min 60)
	Env         []string `yaml:"env"`
	Disabled    bool     `yaml:"disabled"`
}

// Options configures the manager.
type Options struct {
	Dir      string // plugins.d directory scanned for *.plugin executables
	Disabled []string
	Logger   *slog.Logger
	// RestartMin/RestartMax bound the exponential back-off between restarts.
	RestartMin time.Duration
	RestartMax time.Duration
}

// State of a plugin process.
type State string

const (
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateWaiting  State = "waiting" // exited, restart scheduled
	StateStopped  State = "stopped" // context cancelled
	StateDisabled State = "disabled"
	StateFailed   State = "failed" // cannot start (missing binary, not executable)
)

// Status is the public view of a plugin for /api/v1/collectors.
type Status struct {
	Name        string             `json:"name"`
	Command     string             `json:"command"`
	State       State              `json:"state"`
	PID         int                `json:"pid,omitempty"`
	Enabled     bool               `json:"enabled"`
	UpdateEvery int                `json:"update_every"`
	Restarts    int                `json:"restarts"`
	Started     int64              `json:"started,omitempty"`
	Error       string             `json:"error,omitempty"`
	Stats       Stats              `json:"stats"`
	Variables   map[string]float64 `json:"variables,omitempty"`
}

type plugin struct {
	spec    Spec
	parser  *Parser
	mu      sync.Mutex
	state   State
	pid     int
	restart int
	started time.Time
	err     string
}

// Manager supervises plugin processes: start, watchdog, restart with
// back-off, stop on shutdown.
type Manager struct {
	reg  *registry.Registry
	opt  Options
	log  *slog.Logger
	mu   sync.Mutex
	list []*plugin
}

// New builds a manager from explicit specs plus every executable
// <Dir>/*.plugin (name = file name without the suffix). Explicit specs win
// over discovered ones with the same name.
func New(reg *registry.Registry, specs []Spec, opt Options) *Manager {
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	if opt.RestartMin <= 0 {
		opt.RestartMin = time.Second
	}
	if opt.RestartMax <= 0 {
		opt.RestartMax = time.Minute
	}
	m := &Manager{reg: reg, opt: opt, log: opt.Logger}
	disabled := map[string]bool{}
	for _, n := range opt.Disabled {
		disabled[n] = true
	}
	seen := map[string]bool{}
	add := func(sp Spec) {
		if sp.Name == "" || seen[sp.Name] {
			return
		}
		seen[sp.Name] = true
		if sp.UpdateEvery <= 0 {
			sp.UpdateEvery = reg.Host.UpdateEvery
		}
		if sp.Timeout <= 0 {
			sp.Timeout = max(10*sp.UpdateEvery, 60)
		}
		// Relative commands resolve against the plugins dir first; a bare name
		// that is not there falls back to PATH.
		if !filepath.IsAbs(sp.Command) && opt.Dir != "" {
			if _, err := os.Stat(filepath.Join(opt.Dir, sp.Command)); err == nil {
				sp.Command = filepath.Join(opt.Dir, sp.Command)
			}
		}
		if strings.ContainsAny(sp.Command, `/\`) {
			if abs, err := filepath.Abs(sp.Command); err == nil {
				sp.Command = abs
			}
		} else if lp, err := exec.LookPath(sp.Command); err == nil { // bare name: search PATH
			sp.Command = lp
		}
		p := &plugin{spec: sp, parser: &Parser{Plugin: sp.Name, Reg: reg}, state: StateStarting}
		if sp.Disabled || disabled[sp.Name] {
			p.state = StateDisabled
		}
		m.list = append(m.list, p)
	}
	for _, sp := range specs {
		add(sp)
	}
	for _, sp := range discover(opt.Dir) {
		add(sp)
	}
	sort.Slice(m.list, func(i, j int) bool { return m.list[i].spec.Name < m.list[j].spec.Name })
	return m
}

// discover lists <dir>/*.plugin (plus .exe/.bat/.cmd variants on Windows).
func discover(dir string) []Spec {
	if dir == "" {
		return nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Spec
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		base := name
		if runtime.GOOS == "windows" {
			ext := strings.ToLower(filepath.Ext(name))
			if ext == ".exe" || ext == ".bat" || ext == ".cmd" {
				base = strings.TrimSuffix(name, filepath.Ext(name))
			}
		}
		if !strings.HasSuffix(base, ".plugin") {
			continue
		}
		if runtime.GOOS != "windows" {
			info, err := e.Info()
			if err != nil || info.Mode()&0o111 == 0 {
				continue
			}
		}
		out = append(out, Spec{Name: strings.TrimSuffix(base, ".plugin"), Command: filepath.Join(dir, name)})
	}
	return out
}

// Status snapshots every plugin.
func (m *Manager) Status() []Status {
	m.mu.Lock()
	list := append([]*plugin(nil), m.list...)
	m.mu.Unlock()
	out := make([]Status, 0, len(list))
	for _, p := range list {
		p.mu.Lock()
		st := Status{
			Name: p.spec.Name, Command: p.spec.Command, State: p.state, PID: p.pid,
			Enabled: p.state != StateDisabled, UpdateEvery: p.spec.UpdateEvery,
			Restarts: p.restart, Error: p.err,
		}
		if !p.started.IsZero() {
			st.Started = p.started.Unix()
		}
		p.mu.Unlock()
		st.Stats = p.parser.Stats()
		if v := p.parser.Variables(); len(v) > 0 {
			st.Variables = v
		}
		out = append(out, st)
	}
	return out
}

// Run starts every enabled plugin and blocks until ctx is done and all
// processes have exited.
func (m *Manager) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, p := range m.list {
		if p.state == StateDisabled {
			continue
		}
		wg.Add(1)
		go func(p *plugin) {
			defer wg.Done()
			m.supervise(ctx, p)
		}(p)
	}
	wg.Wait()
}

func (m *Manager) set(p *plugin, st State, err string) {
	p.mu.Lock()
	p.state, p.err = st, err
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
			m.log.Warn("plugin cannot start", "plugin", p.spec.Name, "err", err)
			return
		}
		msg := "exited"
		if err != nil {
			msg = err.Error()
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
	if _, err := os.Stat(p.spec.Command); err != nil {
		return fmt.Errorf("%w: %v", errNotStartable, err)
	}
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	args := append([]string{strconv.Itoa(p.spec.UpdateEvery)}, p.spec.Args...)
	cmd := exec.CommandContext(pctx, p.spec.Command, args...)
	cmd.Env = append(os.Environ(),
		"MONITOR_UPDATE_EVERY="+strconv.Itoa(p.spec.UpdateEvery),
		"MONITOR_HOSTNAME="+m.reg.Host.Hostname,
		"MONITOR_PLUGIN="+p.spec.Name,
		"NETDATA_UPDATE_EVERY="+strconv.Itoa(p.spec.UpdateEvery), // many existing plugins read this
	)
	cmd.Env = append(cmd.Env, p.spec.Env...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	cmd.Stdin = nil
	cmd.WaitDelay = 3 * time.Second // then force-close pipes held open by orphaned children
	setProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		var pe *os.PathError
		if errors.As(err, &pe) || errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("%w: %v", errNotStartable, err)
		}
		return err
	}
	p.mu.Lock()
	p.state, p.pid, p.started, p.err = StateRunning, cmd.Process.Pid, time.Now(), ""
	p.mu.Unlock()
	m.log.Info("plugin started", "plugin", p.spec.Name, "pid", cmd.Process.Pid, "cmd", p.spec.Command)

	// stderr → log
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 4096), 64<<10)
		for sc.Scan() {
			m.log.Info("plugin stderr", "plugin", p.spec.Name, "msg", sc.Text())
		}
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
				if time.Since(p.parser.lastActivity()) > timeout {
					wdErr = fmt.Errorf("no output for %s", timeout)
					cancel()
					return
				}
			}
		}
	}()

	// Wait runs concurrently so WaitDelay can unblock the stdout reader when
	// the process is gone but a child still holds the pipe.
	waitc := make(chan error, 1)
	go func() { waitc <- cmd.Wait() }()

	p.parser.touch()
	perr := p.parser.Run(stdout)
	cancel()
	<-wdDone
	werr := <-waitc
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
