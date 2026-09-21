package collect

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// intelgpuConfig is collectors.modules.intelgpu (`intel_gpu_top -J`).
type intelgpuConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type intelgpuCollector struct {
	cfg  intelgpuConfig
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	seen map[string]bool
}

func init() {
	Register("intelgpu", func() Collector { return &intelgpuCollector{} })
}

func (i *intelgpuCollector) Name() string { return "intelgpu" }

func (i *intelgpuCollector) Configure(decode func(v any) error) error {
	if err := decode(&i.cfg); err != nil {
		return err
	}
	if i.cfg.Command == "" {
		i.cfg.Command = "intel_gpu_top"
	}
	if i.cfg.Timeout <= 0 {
		i.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (i *intelgpuCollector) Init(reg *registry.Registry) error {
	if i.cfg.Command == "" {
		if err := i.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if i.run == nil {
		i.run = execRun(i.cfg.Timeout)
	}
	i.seen = map[string]bool{}
	if _, err := i.sample(context.Background()); err != nil {
		return err
	}
	for _, ch := range []*registry.Chart{
		{ID: "intelgpu.frequency", Title: "Intel GPU frequency", Units: "MHz", Priority: 61100,
			Dimensions: []*registry.Dimension{{ID: "frequency"}}},
		{ID: "intelgpu.power", Title: "Intel GPU power", Units: "Watts", Priority: 61110,
			Dimensions: []*registry.Dimension{{ID: "gpu"}, {ID: "package"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "intelgpu", "intelgpu", "intelgpu"
		reg.AddChart(ch)
	}
	return nil
}

func (i *intelgpuCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := i.sample(ctx)
	if err != nil {
		return err
	}
	freq := nestFloat(m, "frequency", "actual")
	if freq == 0 {
		freq = nestFloat(m, "frequency", "requested")
	}
	_ = reg.Collect("intelgpu.frequency", now, map[string]float64{"frequency": freq})
	pwr := nestMap(m, "power")
	_ = reg.Collect("intelgpu.power", now, map[string]float64{"gpu": nestFloat(pwr, "GPU"), "package": nestFloat(pwr, "Package")})
	engines := nestMap(m, "engines")
	for name, v := range engines {
		mm, _ := v.(map[string]any)
		id := sanitizeID(strings.ToLower(strings.ReplaceAll(name, "/", "_")))
		if !i.seen[id] {
			i.seen[id] = true
			ch := &registry.Chart{ID: "intelgpu.engine_busy_perc." + id, Context: "intelgpu.engine_busy_perc",
				Title: "Intel GPU engine busy time percentage", Units: "percentage", Priority: 61120,
				Dimensions: []*registry.Dimension{{ID: "busy"}}}
			ch.Family, ch.Plugin, ch.Module = "intelgpu", "intelgpu", "intelgpu"
			reg.AddChart(ch)
		}
		_ = reg.Collect("intelgpu.engine_busy_perc."+id, now, map[string]float64{"busy": nestFloat(mm, "busy")})
	}
	return nil
}

func (i *intelgpuCollector) sample(ctx context.Context) (map[string]any, error) {
	b, err := i.run(ctx, i.cfg.Command, "-J", "-s", "100", "-o", "-")
	if err != nil {
		return nil, fmt.Errorf("intelgpu: %w", err)
	}
	s := strings.TrimSpace(string(b))
	if i := strings.Index(s, "{"); i >= 0 {
		s = s[i:]
	}
	if j := strings.LastIndex(s, "}"); j >= 0 {
		s = s[:j+1]
	}
	m, err := jsonMap([]byte(s))
	if err != nil {
		return nil, fmt.Errorf("intelgpu: %w", err)
	}
	if nestMap(m, "frequency") == nil && nestMap(m, "engines") == nil && nestMap(m, "power") == nil {
		return nil, fmt.Errorf("intelgpu: no gpu stats")
	}
	return m, nil
}
