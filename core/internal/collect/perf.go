package collect

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// perfConfig is collectors.modules.perf (Netdata perf.plugin via `perf stat`).
type perfConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
	Events  string        `yaml:"events"`
}

type perfCollector struct {
	cfg    perfConfig
	run    func(ctx context.Context, name string, args ...string) ([]byte, error)
	sample func(ctx context.Context) (map[string]float64, error)
	native *perfHardware
}

func init() {
	Register("perf", func() Collector { return &perfCollector{} })
}

func (p *perfCollector) Name() string { return "perf" }

func (p *perfCollector) Configure(decode func(v any) error) error {
	if err := decode(&p.cfg); err != nil {
		return err
	}
	if p.cfg.Command == "" {
		p.cfg.Command = "perf"
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 3 * time.Second
	}
	if p.cfg.Events == "" {
		p.cfg.Events = "cycles,instructions,cache-references,cache-misses,branches,branch-misses"
	}
	return nil
}

func (p *perfCollector) Init(reg *registry.Registry) error {
	if p.cfg.Command == "" {
		if err := p.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if p.run == nil {
		p.run = execRun(p.cfg.Timeout)
	}
	if p.sample == nil {
		if native, err := openPerfHardware(); err == nil {
			p.native = native
			p.sample = native.sample
		} else {
			p.sample = p.perfStat
		}
	}
	if _, err := p.sample(context.Background()); err != nil {
		return fmt.Errorf("perf: unavailable: %w", err)
	}
	cpu := sysChart("perf.cpu", "perf", "CPU hardware events", "events/s", 1400,
		incDim("cycles"), incDim("instructions"), incDim("cache_references"), incDim("cache_misses"),
		incDim("branches"), incDim("branch_misses"))
	cpu.Plugin, cpu.Module, cpu.Family = "perf", "perf", "perf"
	reg.AddChart(cpu)
	ipc := sysChart("perf.instructions", "perf", "Instructions retired", "instructions/s", 1401, incDim("instructions"))
	ipc.Plugin, ipc.Module, ipc.Family = "perf", "perf", "perf"
	reg.AddChart(ipc)
	miss := sysChart("perf.cache_misses", "perf", "Cache misses", "misses/s", 1402, incDim("cache_misses"))
	miss.Plugin, miss.Module, miss.Family = "perf", "perf", "perf"
	reg.AddChart(miss)
	return nil
}

func (p *perfCollector) Stop() {
	if p.native != nil {
		p.native.close()
	}
}

func (p *perfCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := p.sample(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("perf.cpu", now, map[string]float64{
		"cycles": m["cycles"], "instructions": m["instructions"],
		"cache_references": m["cache_references"], "cache_misses": m["cache_misses"],
		"branches": m["branches"], "branch_misses": m["branch_misses"],
	})
	_ = reg.Collect("perf.instructions", now, map[string]float64{"instructions": m["instructions"]})
	_ = reg.Collect("perf.cache_misses", now, map[string]float64{"cache_misses": m["cache_misses"]})
	return nil
}

func (p *perfCollector) perfStat(ctx context.Context) (map[string]float64, error) {
	out, err := p.run(ctx, p.cfg.Command, "stat", "-a", "-x,", "-e", p.cfg.Events, "--timeout", "150")
	if err != nil && len(out) == 0 {
		return nil, err
	}
	m := parsePerfStat(string(out))
	if len(m) == 0 {
		return nil, fmt.Errorf("perf: empty stat")
	}
	return m, nil
}

func parsePerfStat(s string) map[string]float64 {
	out := map[string]float64{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 3 {
			continue
		}
		n := firstFloat(parts[0])
		if n == 0 && parts[0] != "0" && !strings.Contains(parts[0], "0") {
			continue
		}
		ev := parts[2]
		if i := strings.IndexByte(ev, ':'); i > 0 {
			ev = ev[:i]
		}
		ev = strings.ReplaceAll(ev, "-", "_")
		out[ev] = n
	}
	return out
}
