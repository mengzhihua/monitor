package collect

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// iopingConfig is collectors.modules.ioping. command "auto" (default) times one
// read of device, then falls back to the ioping binary. "native" does not
// fall back. The read never writes.
type iopingConfig struct {
	Command string        `yaml:"command"`
	Device  string        `yaml:"device"`
	Timeout time.Duration `yaml:"timeout"`
}

type iopingCollector struct {
	cfg    iopingConfig
	run    func(ctx context.Context, name string, args ...string) ([]byte, error)
	native bool
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
		i.cfg.Command = "auto"
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
	if i.run == nil && i.cfg.Command != "ioping" {
		if _, err := measureIOLatency(i.cfg.Device); err == nil {
			i.native = true
			i.addCharts(reg)
			return nil
		} else if i.cfg.Command == "native" {
			return fmt.Errorf("ioping: %w", err)
		}
	}
	if i.run == nil {
		i.run = execRun(i.cfg.Timeout)
	}
	cmd := i.cfg.Command
	if cmd == "auto" || cmd == "native" || cmd == "" {
		cmd = "ioping"
	}
	if _, err := i.run(context.Background(), cmd, "-c", "1", "-q", i.cfg.Device); err != nil {
		return fmt.Errorf("ioping: %w", err)
	}
	i.addCharts(reg)
	return nil
}

func (i *iopingCollector) addCharts(reg *registry.Registry) {
	lat := sysChart("ioping.latency", "ioping", "IOPing latency", "microseconds", 38000,
		&registry.Dimension{ID: "min"}, &registry.Dimension{ID: "avg"}, &registry.Dimension{ID: "max"})
	lat.Plugin, lat.Module, lat.Family = "ioping", "ioping", "ioping"
	reg.AddChart(lat)
	iops := sysChart("ioping.iops", "ioping", "IOPing IOPS", "iops", 38001, &registry.Dimension{ID: "iops"})
	iops.Plugin, iops.Module, iops.Family = "ioping", "ioping", "ioping"
	reg.AddChart(iops)
}

func (i *iopingCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	var st iopingStats
	if i.native {
		var err error
		st, err = measureIOLatency(i.cfg.Device)
		if err != nil {
			return err
		}
	} else {
		cmd := i.cfg.Command
		if cmd == "auto" || cmd == "native" || cmd == "" {
			cmd = "ioping"
		}
		raw, err := i.run(ctx, cmd, "-c", "1", "-q", i.cfg.Device)
		if err != nil {
			return err
		}
		st = parseIoping(string(raw))
	}
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

func measureIOLatency(path string) (iopingStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return iopingStats{}, err
	}
	defer f.Close()
	_ = f.SetDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	start := time.Now()
	_, err = f.Read(buf)
	elapsed := time.Since(start)
	if err != nil && err != io.EOF {
		return iopingStats{}, err
	}
	us := float64(elapsed.Microseconds())
	if us < 1 {
		us = 1
	}
	return iopingStats{Min: us, Avg: us, Max: us, IOPS: 1e6 / us}, nil
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
