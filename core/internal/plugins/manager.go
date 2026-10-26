package plugins

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// Spec describes one external plugin process. Execution fields are kept private
// to the manager; only Status is suitable for public responses.
type Spec struct {
	Name        string    `yaml:"name"`
	Command     string    `yaml:"command"`
	Args        []string  `yaml:"args"`
	UpdateEvery int       `yaml:"update_every"`
	Timeout     int       `yaml:"timeout"`
	Env         []string  `yaml:"env"`
	Disabled    bool      `yaml:"disabled"`
	Source      string    `yaml:"-"`
	Manifest    *Manifest `yaml:"-"`
	// Per-plugin ceilings. Zero means the process budget for a manifest package,
	// and no chart/dimension/label/variable cap for a legacy script. A positive
	// value cannot raise the process budget. max_line still applies to every plugin.
	MaxCharts     int `yaml:"max_charts,omitempty" json:"max_charts,omitempty"`
	MaxDimensions int `yaml:"max_dimensions,omitempty" json:"max_dimensions,omitempty"`
	MaxLabels     int `yaml:"max_labels,omitempty" json:"max_labels,omitempty"`
	MaxVariables  int `yaml:"max_variables,omitempty" json:"max_variables,omitempty"`
	MaxLine       int `yaml:"max_line,omitempty" json:"max_line,omitempty"`
}

// PackageBudgets are the manifest-package resource limits reported by the
// catalog. Zero fields mean the built-in defaults. Legacy plugins.d scripts
// are not subject to the chart, dimension, label or variable caps.
type PackageBudgets struct {
	MaxCharts     int `json:"max_charts"`
	MaxDimensions int `json:"max_dimensions"`
	MaxLabels     int `json:"max_labels"`
	MaxVariables  int `json:"max_variables"`
	MaxLine       int `json:"max_line"`
}

// Effective fills zero fields with the built-in package ceilings.
func (b PackageBudgets) Effective() PackageBudgets {
	if b.MaxCharts <= 0 {
		b.MaxCharts = 128
	}
	if b.MaxDimensions <= 0 {
		b.MaxDimensions = 64
	}
	if b.MaxLabels <= 0 {
		b.MaxLabels = 128
	}
	if b.MaxVariables <= 0 {
		b.MaxVariables = 128
	}
	if b.MaxLine <= 0 {
		b.MaxLine = 64 << 10
	}
	return b
}

// Options configures discovery, persisted controls and restart backoff.
type Options struct {
	Dir           string
	Disabled      []string
	StatePath     string
	Logger        *slog.Logger
	RestartMin    time.Duration
	RestartMax    time.Duration
	MaxCharts     int
	MaxDimensions int
	MaxLabels     int
	MaxVariables  int
	MaxLine       int
	// Functions enables calls for packages that declare the functions capability.
	// Nil means enabled. False rejects FUNCTION lines and hides the functions.
	Functions *bool
}

func (o Options) functionsEnabled() bool { return o.Functions == nil || *o.Functions }

func (m *Manager) parserFor(spec Spec) *Parser {
	p := newPluginParser(spec, m.reg, m.opt.packageBudgets())
	if m.opt.functionsEnabled() && spec.Manifest.allowsFunctions() {
		p.AllowsFunctions = true
	}
	return p
}

func (o Options) packageBudgets() PackageBudgets {
	return PackageBudgets{
		MaxCharts: o.MaxCharts, MaxDimensions: o.MaxDimensions,
		MaxLabels: o.MaxLabels, MaxVariables: o.MaxVariables, MaxLine: o.MaxLine,
	}.Effective()
}

func (b PackageBudgets) tighten(limit PackageBudgets) PackageBudgets {
	if limit.MaxCharts > 0 && limit.MaxCharts < b.MaxCharts {
		b.MaxCharts = limit.MaxCharts
	}
	if limit.MaxDimensions > 0 && limit.MaxDimensions < b.MaxDimensions {
		b.MaxDimensions = limit.MaxDimensions
	}
	if limit.MaxLabels > 0 && limit.MaxLabels < b.MaxLabels {
		b.MaxLabels = limit.MaxLabels
	}
	if limit.MaxVariables > 0 && limit.MaxVariables < b.MaxVariables {
		b.MaxVariables = limit.MaxVariables
	}
	if limit.MaxLine > 0 && limit.MaxLine < b.MaxLine {
		b.MaxLine = limit.MaxLine
	}
	return b
}

func (sp Spec) declaredLimits() PackageBudgets {
	return PackageBudgets{
		MaxCharts: sp.MaxCharts, MaxDimensions: sp.MaxDimensions,
		MaxLabels: sp.MaxLabels, MaxVariables: sp.MaxVariables, MaxLine: sp.MaxLine,
	}
}

// enforcedBudgets is the ceiling actually applied to this plugin. Manifest
// packages always receive a full budget. Legacy scripts stay uncapped unless
// the operator set a per-plugin ceiling; max_line always has a value.
func enforcedBudgets(sp Spec, process PackageBudgets) (PackageBudgets, bool) {
	process = process.Effective().tighten(sp.declaredLimits())
	if sp.Manifest != nil {
		return process, true
	}
	if sp.MaxCharts == 0 && sp.MaxDimensions == 0 && sp.MaxLabels == 0 && sp.MaxVariables == 0 && sp.MaxLine == 0 {
		return PackageBudgets{}, false
	}
	out := PackageBudgets{MaxLine: process.MaxLine}
	if sp.MaxCharts > 0 {
		out.MaxCharts = process.MaxCharts
	}
	if sp.MaxDimensions > 0 {
		out.MaxDimensions = process.MaxDimensions
	}
	if sp.MaxLabels > 0 {
		out.MaxLabels = process.MaxLabels
	}
	if sp.MaxVariables > 0 {
		out.MaxVariables = process.MaxVariables
	}
	return out, true
}

type State string

const (
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateWaiting  State = "waiting"
	StateStopped  State = "stopped"
	StateDisabled State = "disabled"
	StateFailed   State = "failed"
)

// Status is the public view used by the collectors and plugin catalog APIs.
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
	Functions   []string           `json:"functions,omitempty"`
	Source      string             `json:"source"`
	Manifest    *Manifest          `json:"manifest,omitempty"`
	Budgets     *PackageBudgets    `json:"budgets,omitempty"`
	Locked      bool               `json:"locked"`
	Stderr      StderrStats        `json:"stderr"`
}

type plugin struct {
	spec        Spec // immutable after discovery
	fingerprint string
	locked      bool
	mu          sync.Mutex
	parser      *Parser
	state       State
	enabled     bool
	pid         int
	restart     int
	started     time.Time
	err         string
	// Worker ownership is serialized by Manager.gate. The worker never edits
	// these fields, including after a self-disable or failed start.
	stderr *stderrState
	cancel context.CancelFunc
	done   chan struct{}
	stdin  io.WriteCloser
	call   chan struct{}
}

// Manager owns plugin workers. Catalog reads use mu; lifecycle and control
// transitions use gate so waiting for a worker never blocks status polling.
type Manager struct {
	reg         *registry.Registry
	opt         Options
	log         *slog.Logger
	specs       []Spec
	gate        chan struct{}
	mu          sync.Mutex
	list        []*plugin
	diagnostics []Diagnostic
	revision    uint64
	disabled    map[string]bool
	initErr     error
	running     bool
	started     bool
	runCtx      context.Context
}

// New builds a catalog in configured > manifest > legacy precedence. A corrupt
// persisted state is fail-closed: Err reports it and Run starts no processes.
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
	opt.RestartMax = max(opt.RestartMin, opt.RestartMax)
	opt.Disabled = append([]string(nil), opt.Disabled...)
	m := &Manager{reg: reg, opt: opt, log: opt.Logger, gate: make(chan struct{}, 1), revision: 1, disabled: map[string]bool{}}
	for _, sp := range specs {
		m.specs = append(m.specs, cloneSpec(sp))
	}
	m.initErr = m.loadState()
	m.list, m.diagnostics, _ = m.discoverCatalog(context.Background())
	return m
}

func (m *Manager) Err() error { return m.initErr }

// Run remains available even when the catalog is empty or every plugin has
// disabled itself. It returns only after cancellation and all workers exit.
// A manager has a single Run lifetime; repeated calls do not start duplicates.
func (m *Manager) Run(ctx context.Context) {
	if err := m.acquire(ctx); err != nil {
		return
	}
	m.mu.Lock()
	if m.started || m.initErr != nil || ctx.Err() != nil {
		m.mu.Unlock()
		m.release()
		return
	}
	m.started, m.running, m.runCtx = true, true, ctx
	list := append([]*plugin(nil), m.list...)
	m.mu.Unlock()
	for _, p := range list {
		m.startWorker(p)
	}
	m.release()
	<-ctx.Done()
	// A committed control action finishes its handoff even if its request ends.
	// Shutdown then cancels every worker before waiting for any one of them.
	_ = m.acquire(context.Background())
	m.mu.Lock()
	m.running = false
	list = append([]*plugin(nil), m.list...)
	m.mu.Unlock()
	for _, p := range list {
		if p.cancel != nil {
			p.cancel()
		}
	}
	for _, p := range list {
		m.stopWorker(p)
	}
	m.release()
}

func (m *Manager) acquire(ctx context.Context) error {
	select {
	case m.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			m.release()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *Manager) release() { <-m.gate }

func (m *Manager) startWorker(p *plugin) {
	p.mu.Lock()
	enabled := p.enabled
	if enabled {
		p.state, p.err = StateStarting, ""
	}
	p.mu.Unlock()
	if !enabled {
		return
	}
	if m.runCtx.Err() != nil {
		m.set(p, StateStopped, "")
		return
	}
	ctx, cancel := context.WithCancel(m.runCtx)
	p.cancel, p.done = cancel, make(chan struct{})
	go func() { defer close(p.done); m.supervise(ctx, p) }()
}

func (m *Manager) stopWorker(p *plugin) {
	if p.cancel == nil {
		return
	}
	p.cancel()
	<-p.done
	p.cancel, p.done = nil, nil
}
