// Package collect defines the collector interface and the scheduler that runs
// every enabled collector once per update interval.
package collect

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// Collector produces samples for one or more charts every tick.
type Collector interface {
	// Name is the collector id, e.g. "cpu".
	Name() string
	// Init registers charts. Returning an error disables the collector
	// (e.g. the data source does not exist on this platform).
	Init(reg *registry.Registry) error
	// Collect gathers one round of raw values and feeds them to the registry.
	Collect(ctx context.Context, reg *registry.Registry, now time.Time) error
}

// Stopper is an optional collector that holds sockets or background work
// (StatsD, long-lived HTTP clients) and must release them on shutdown.
type Stopper interface {
	Stop()
}

// WeightProvider ranks charts (Anomaly Advisor / metric correlations).
type WeightProvider interface {
	Weights(method string) []Weight
}

// Weight is one row of GET /api/v1/weights.
type Weight struct {
	Chart       string  `json:"chart"`
	Context     string  `json:"context"`
	Title       string  `json:"title"`
	Dimension   string  `json:"dimension,omitempty"`
	Score       float64 `json:"score"`
	AnomalyRate float64 `json:"anomaly_rate"`
}

// AnomalyProvider exposes the current per-dimension anomaly bit (series id → flag).
type AnomalyProvider interface {
	DimAnomalies() map[string]bool
}

// Configurable collectors receive their `collectors.modules.<name>` section
// before Init. decode unmarshals the section into v (yaml semantics).
type Configurable interface {
	Configure(decode func(v any) error) error
}

// Function is an on-demand routine exposed via /api/v1/function (e.g. a live
// process table). Collectors implementing FunctionProvider are asked for
// theirs at scheduler construction.
type Function struct {
	Name    string `json:"name"`
	Help    string `json:"help"`
	Timeout int    `json:"timeout"` // seconds
	Run     func(ctx context.Context, args map[string]string) (any, error)
}

type FunctionProvider interface {
	Functions() []Function
}

// Options controls which collectors the scheduler instantiates.
type Options struct {
	Names    []string        // empty = all registered
	Disabled map[string]bool // names turned off in config
	// Modules supplies a decoder for every collector that has a config
	// section; nil entries mean "use defaults".
	Modules map[string]func(v any) error
	// Timeout bounds one Collect call; defaults to the update interval.
	Timeout time.Duration
}

type Factory func() Collector

var (
	regMu     sync.Mutex
	factories = map[string]Factory{}
)

// Register makes a collector available to the scheduler; called from init().
func Register(name string, f Factory) {
	regMu.Lock()
	defer regMu.Unlock()
	factories[name] = f
}

func Available() []string {
	regMu.Lock()
	defer regMu.Unlock()
	out := make([]string, 0, len(factories))
	for n := range factories {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

type Status struct {
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Error    string `json:"error,omitempty"`
	Runs     uint64 `json:"runs"`
	Failures uint64 `json:"failures"`
	LastRun  int64  `json:"last_run_ms"`
}

type running struct {
	c      Collector
	status Status
	mu     sync.Mutex
}

// Scheduler ticks every reg.Host.UpdateEvery seconds and runs all collectors
// concurrently, each bounded by a per-tick timeout.
type Scheduler struct {
	reg     *registry.Registry
	log     *slog.Logger
	timeout time.Duration
	cols    []*running
	funcs   []Function
}

// NewScheduler instantiates the selected collectors, configures and
// initialises them; collectors whose Configure/Init fails are kept in the
// status list as disabled.
func NewScheduler(reg *registry.Registry, log *slog.Logger, opt Options) *Scheduler {
	names := opt.Names
	if len(names) == 0 {
		names = Available()
	}
	if log == nil {
		log = slog.Default()
	}
	if opt.Timeout <= 0 {
		opt.Timeout = time.Duration(reg.Host.UpdateEvery) * time.Second
	}
	s := &Scheduler{reg: reg, log: log, timeout: opt.Timeout}
	regMu.Lock()
	defer regMu.Unlock()
	for n := range opt.Modules {
		if _, ok := factories[n]; !ok {
			log.Warn("collector: config for unknown module", "name", n)
		}
	}
	for _, n := range names {
		f, ok := factories[n]
		if !ok {
			log.Warn("collector: unknown", "name", n)
			continue
		}
		if opt.Disabled[n] {
			s.cols = append(s.cols, &running{c: f(), status: Status{Name: n}})
			continue
		}
		c := f()
		r := &running{c: c, status: Status{Name: n, Enabled: true}}
		err := configure(c, opt.Modules[n])
		if err == nil {
			err = c.Init(reg)
		}
		if err != nil {
			r.status.Enabled = false
			r.status.Error = err.Error()
			log.Info("collector: disabled", "name", n, "reason", err)
		} else if fp, ok := c.(FunctionProvider); ok {
			s.funcs = append(s.funcs, fp.Functions()...)
		}
		s.cols = append(s.cols, r)
	}
	return s
}

func configure(c Collector, decode func(v any) error) error {
	cc, ok := c.(Configurable)
	if !ok {
		return nil
	}
	if decode == nil {
		decode = func(any) error { return nil }
	}
	return cc.Configure(decode)
}

// Functions lists the on-demand functions offered by enabled collectors.
func (s *Scheduler) Functions() []Function {
	out := make([]Function, len(s.funcs))
	copy(out, s.funcs)
	return out
}

func (s *Scheduler) Status() []Status {
	out := make([]Status, 0, len(s.cols))
	for _, r := range s.cols {
		r.mu.Lock()
		out = append(out, r.status)
		r.mu.Unlock()
	}
	return out
}

// Collector returns a live collector by name (even if it failed to Init).
func (s *Scheduler) Collector(name string) Collector {
	for _, r := range s.cols {
		if r.c.Name() == name {
			return r.c
		}
	}
	return nil
}

// SetEnabled turns a collector on or off at runtime (hub config push).
func (s *Scheduler) SetEnabled(name string, on bool) bool {
	for _, r := range s.cols {
		if r.c.Name() != name {
			continue
		}
		r.mu.Lock()
		r.status.Enabled = on
		if on {
			r.status.Error = ""
		}
		r.mu.Unlock()
		return true
	}
	return false
}

// Run blocks until ctx is done. The first tick is aligned to the next whole
// interval boundary so samples land on round timestamps.
func (s *Scheduler) Run(ctx context.Context) {
	defer s.stop()
	every := time.Duration(s.reg.Host.UpdateEvery) * time.Second
	s.tick(ctx, time.Now())
	next := time.Now().Truncate(every).Add(every)
	timer := time.NewTimer(time.Until(next))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-timer.C:
			s.tick(ctx, now.Truncate(every))
			next = next.Add(every)
			for next.Before(time.Now()) { // we fell behind: skip
				next = next.Add(every)
			}
			timer.Reset(time.Until(next))
		}
	}
}

func (s *Scheduler) stop() {
	for _, r := range s.cols {
		if st, ok := r.c.(Stopper); ok {
			st.Stop()
		}
	}
}

func (s *Scheduler) tick(ctx context.Context, now time.Time) {
	var wg sync.WaitGroup
	for _, r := range s.cols {
		if !r.status.Enabled {
			continue
		}
		wg.Add(1)
		go func(r *running) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, s.timeout)
			defer cancel()
			start := time.Now()
			err := r.c.Collect(cctx, s.reg, now)
			r.mu.Lock()
			r.status.Runs++
			r.status.LastRun = time.Since(start).Milliseconds()
			if err != nil {
				r.status.Failures++
				r.status.Error = err.Error()
				if r.status.Failures%60 == 1 {
					s.log.Warn("collector: failed", "name", r.c.Name(), "err", err)
				}
			} else {
				r.status.Error = ""
			}
			r.mu.Unlock()
		}(r)
	}
	wg.Wait()
}
