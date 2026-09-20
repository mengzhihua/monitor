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
}

// NewScheduler instantiates the named collectors (all registered ones when
// names is empty) and initialises them; collectors whose Init fails are kept
// in the status list as disabled.
func NewScheduler(reg *registry.Registry, log *slog.Logger, names []string, disabled map[string]bool) *Scheduler {
	if len(names) == 0 {
		names = Available()
	}
	if log == nil {
		log = slog.Default()
	}
	s := &Scheduler{reg: reg, log: log, timeout: time.Duration(reg.Host.UpdateEvery) * time.Second}
	regMu.Lock()
	defer regMu.Unlock()
	for _, n := range names {
		f, ok := factories[n]
		if !ok {
			log.Warn("collector: unknown", "name", n)
			continue
		}
		if disabled[n] {
			s.cols = append(s.cols, &running{c: f(), status: Status{Name: n}})
			continue
		}
		c := f()
		r := &running{c: c, status: Status{Name: n, Enabled: true}}
		if err := c.Init(reg); err != nil {
			r.status.Enabled = false
			r.status.Error = err.Error()
			log.Info("collector: disabled", "name", n, "reason", err)
		}
		s.cols = append(s.cols, r)
	}
	return s
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

// Run blocks until ctx is done. The first tick is aligned to the next whole
// interval boundary so samples land on round timestamps.
func (s *Scheduler) Run(ctx context.Context) {
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
