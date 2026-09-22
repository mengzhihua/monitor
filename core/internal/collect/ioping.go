package collect

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// iopingConfig is collectors.modules.ioping (Netdata ioping.plugin).
type iopingConfig struct {
	Command string        `yaml:"command"`
	Device  string        `yaml:"device"`
	Timeout time.Duration `yaml:"timeout"`
}

type iopingCollector struct {
	cfg iopingConfig
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func init() {
	Register("ioping", func() Collector { return &iopingCollector{} })
}

func (i *iopingCollector) Name() string { return "ioping" }

func (i *iopingCollector) Configure(decode func(v any) error) error {
	if err := decode(&i.cfg); err != nil {
		return err
	}
	if i.cfg.Command == "" {
		i.cfg.Command = "ioping"
	}
	if i.cfg.Timeout <= 0 {
		i.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (i *iopingCollector) Init(reg *registry.Registry) error {
	if i.cfg.Command == "" {
		if err := i.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if i.cfg.Device == "" {
		return fmt.Errorf("ioping: device not configured")
	}
	if i.run == nil {
		i.run = execRun(i.cfg.Timeout)
	}
	if _, err := i.run(context.Background(), i.cfg.Command, "-c", "1", "-q", i.cfg.Device); err != nil {
		return fmt.Errorf("ioping: %w", err)
	}
	lat := sysChart("ioping.latency", "ioping", "IOPing latency", "microseconds", 38000,
		&registry.Dimension{ID: "min"}, &registry.Dimension{ID: "avg"}, &registry.Dimension{ID: "max"})
	lat.Plugin, lat.Module, lat.Family = "ioping", "ioping", "ioping"
	reg.AddChart(lat)
	iops := sysChart("ioping.iops", "ioping", "IOPing IOPS", "iops", 38001, &registry.Dimension{ID: "iops"})
	iops.Plugin, iops.Module, iops.Family = "ioping", "ioping", "ioping"
	reg.AddChart(iops)
	return nil
}

func (i *iopingCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	raw, err := i.run(ctx, i.cfg.Command, "-c", "1", "-q", i.cfg.Device)
	if err != nil {
		return err
	}
	st := parseIoping(string(raw))
	_ = reg.Collect("ioping.latency", now, map[string]float64{"min": st.Min, "avg": st.Avg, "max": st.Max})
	_ = reg.Collect("ioping.iops", now, map[string]float64{"iops": st.IOPS})
	return nil
}

type iopingStats struct{ Min, Avg, Max, IOPS float64 }

func parseIoping(s string) iopingStats {
	var out iopingStats
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		low := strings.ToLower(line)
		if i := strings.Index(low, "iops"); i > 0 {
			// take the last number before "iops" (e.g. "1.00 iops")
			out.IOPS = firstFloat(strings.TrimSpace(line[:i]))
			fields := strings.Fields(strings.ReplaceAll(line[:i], ",", " "))
			if len(fields) > 0 {
				out.IOPS = firstFloat(fields[len(fields)-1])
			}
		}
		if strings.Contains(low, "min/avg/max") {
			_, rest, _ := strings.Cut(line, "=")
			parts := strings.Split(rest, "/")
			if len(parts) >= 3 {
				out.Min = parseLatencyUS(parts[0])
				out.Avg = parseLatencyUS(parts[1])
				out.Max = parseLatencyUS(parts[2])
			}
		}
	}
	return out
}

func parseLatencyUS(s string) float64 {
	s = strings.TrimSpace(s)
	n := firstFloat(s)
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "ms"):
		return n * 1000
	case strings.Contains(low, "us") || strings.Contains(low, "µs"):
		return n
	case strings.Contains(low, "ns"):
		return n / 1000
	case strings.Contains(low, "s") && !strings.Contains(low, "ms"):
		return n * 1e6
	default:
		return n
	}
}
