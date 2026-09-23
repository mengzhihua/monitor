package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// smartctlConfig is collectors.modules.smartctl (Netdata go.d smartctl).
type smartctlConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type smartctlCollector struct {
	cfg     smartctlConfig
	run     func(ctx context.Context, name string, args ...string) ([]byte, error)
	devSeen map[string]bool
	last    time.Time
}

// smartctlEvery keeps smartctl off the 1s tick. SMART attributes move slowly,
// and each device is its own process.
const smartctlEvery = 30 * time.Second

func init() {
	Register("smartctl", func() Collector { return &smartctlCollector{} })
}

func (s *smartctlCollector) Name() string { return "smartctl" }

func (s *smartctlCollector) Configure(decode func(v any) error) error {
	if err := decode(&s.cfg); err != nil {
		return err
	}
	if s.cfg.Command == "" {
		s.cfg.Command = "smartctl"
	}
	if s.cfg.Timeout <= 0 {
		s.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (s *smartctlCollector) Init(reg *registry.Registry) error {
	if s.cfg.Command == "" {
		if err := s.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	s.devSeen = map[string]bool{}
	devs, err := s.scan(context.Background())
	if err != nil {
		return err
	}
	if len(devs) == 0 {
		return fmt.Errorf("smartctl: no devices")
	}
	return nil
}

func (s *smartctlCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	if !sampleDue(&s.last, now, smartctlEvery) {
		return nil
	}
	devs, err := s.scan(ctx)
	if err != nil {
		return err
	}
	var last error
	ok := 0
	for _, d := range devs {
		info, err := s.device(ctx, d)
		if err != nil {
			last = err
			continue
		}
		s.ensure(reg, info.Name)
		id := sanitizeID(info.Name)
		passed, failed := 1.0, 0.0
		if !info.Passed {
			passed, failed = 0, 1
		}
		_ = reg.Collect("smartctl.device_smart_status."+id, now, map[string]float64{"passed": passed, "failed": failed})
		_ = reg.Collect("smartctl.device_temperature."+id, now, map[string]float64{"temperature": info.Temp})
		_ = reg.Collect("smartctl.device_power_on_time."+id, now, map[string]float64{"power_on_time": info.PowerOn})
		_ = reg.Collect("smartctl.device_power_cycles_count."+id, now, map[string]float64{"power": info.Cycles})
		ok++
	}
	if ok == 0 {
		if last != nil {
			return last
		}
		return fmt.Errorf("smartctl: no device data")
	}
	return nil
}

type smartDev struct {
	Name                  string
	Passed                bool
	Temp, PowerOn, Cycles float64
}

func (s *smartctlCollector) exec(ctx context.Context, args ...string) ([]byte, error) {
	run := s.run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
			defer cancel()
			return exec.CommandContext(cctx, name, args...).Output()
		}
	}
	return run(ctx, s.cfg.Command, args...)
}

func (s *smartctlCollector) scan(ctx context.Context) ([]string, error) {
	out, err := s.exec(ctx, "--scan")
	if err != nil {
		return nil, fmt.Errorf("smartctl --scan: %w", err)
	}
	devs := parseSmartScan(string(out))
	if len(devs) == 0 {
		return nil, fmt.Errorf("smartctl: no devices")
	}
	return devs, nil
}

func (s *smartctlCollector) device(ctx context.Context, dev string) (smartDev, error) {
	out, err := s.exec(ctx, "-j", "-H", "-A", "-l", "error", dev)
	if err != nil && len(out) == 0 {
		return smartDev{}, err
	}
	info, err := parseSmartJSON(out)
	if err != nil {
		info, err = parseSmartText(string(out), dev)
	}
	if err != nil {
		return smartDev{}, err
	}
	if info.Name == "" {
		info.Name = strings.TrimPrefix(dev, "/dev/")
	}
	return info, nil
}

func (s *smartctlCollector) ensure(reg *registry.Registry, name string) {
	id := sanitizeID(name)
	if s.devSeen[id] {
		return
	}
	s.devSeen[id] = true
	lbl := map[string]string{"device": name}
	mk := func(suffix, title, units string, prio int, dims ...*registry.Dimension) {
		reg.AddChart(&registry.Chart{ID: "smartctl." + suffix + "." + id, Context: "smartctl." + suffix,
			Family: "smartctl", Title: title + " " + name, Units: units, Priority: prio,
			Plugin: "smartctl", Module: "smartctl", Labels: lbl, Dimensions: dims})
	}
	mk("device_smart_status", "Device SMART status", "status", 49000,
		&registry.Dimension{ID: "passed"}, &registry.Dimension{ID: "failed"})
	mk("device_temperature", "Device temperature", "Celsius", 49010, &registry.Dimension{ID: "temperature"})
	mk("device_power_on_time", "Device power on time", "seconds", 49020, &registry.Dimension{ID: "power_on_time"})
	mk("device_power_cycles_count", "Device power cycles", "cycles", 49030, &registry.Dimension{ID: "power"})
}

func parseSmartScan(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		out = append(out, f[0])
	}
	return out
}

func parseSmartJSON(b []byte) (smartDev, error) {
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return smartDev{}, err
	}
	d := smartDev{}
	if n, ok := raw["device"].(map[string]any); ok {
		if name, ok := n["name"].(string); ok {
			d.Name = strings.TrimPrefix(name, "/dev/")
		}
	}
	if st, ok := raw["smart_status"].(map[string]any); ok {
		if p, ok := st["passed"].(bool); ok {
			d.Passed = p
		}
	}
	if t, ok := raw["temperature"].(map[string]any); ok {
		d.Temp = jsonNum(t["current"])
	}
	if p, ok := raw["power_on_time"].(map[string]any); ok {
		d.PowerOn = jsonNum(p["hours"]) * 3600
	}
	d.Cycles = jsonNum(raw["power_cycle_count"])
	return d, nil
}

func parseSmartText(s, dev string) (smartDev, error) {
	d := smartDev{Name: strings.TrimPrefix(dev, "/dev/"), Passed: true}
	found := false
	for _, line := range strings.Split(s, "\n") {
		l := strings.ToLower(line)
		switch {
		case strings.Contains(l, "smart overall-health") || strings.Contains(l, "smart health status"):
			found = true
			d.Passed = strings.Contains(l, "passed") || strings.Contains(l, "ok")
		case strings.Contains(l, "temperature"):
			if v := firstFloat(line); v != 0 {
				d.Temp = v
			}
		case strings.Contains(l, "power_on_hours") || strings.Contains(l, "power-on hours"):
			d.PowerOn = firstFloat(line) * 3600
			found = true
		case strings.Contains(l, "power_cycle_count") || strings.Contains(l, "power cycle"):
			d.Cycles = firstFloat(line)
		}
	}
	if !found && d.Temp == 0 {
		return smartDev{}, fmt.Errorf("smartctl: cannot parse %s", dev)
	}
	return d, nil
}

func jsonNum(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case json.Number:
		f, _ := t.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	case int:
		return float64(t)
	}
	return 0
}
