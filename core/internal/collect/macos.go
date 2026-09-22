package collect

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// macosConfig is collectors.modules.macos (mach host metrics; non-Darwin disables).
type macosConfig struct {
	Command string        `yaml:"command"` // sysctl
	Timeout time.Duration `yaml:"timeout"`
}

type macosCollector struct {
	cfg macosConfig
	run func(ctx context.Context, name string, args ...string) ([]byte, error)

	havePressure, haveSwap, haveThermal, haveBattery bool
}

func init() {
	Register("macos", func() Collector { return &macosCollector{} })
}

func (m *macosCollector) Name() string { return "macos" }

func (m *macosCollector) Configure(decode func(v any) error) error {
	if err := decode(&m.cfg); err != nil {
		return err
	}
	if m.cfg.Command == "" {
		m.cfg.Command = "sysctl"
	}
	if m.cfg.Timeout <= 0 {
		m.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (m *macosCollector) Init(reg *registry.Registry) error {
	if m.cfg.Command == "" {
		if err := m.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if runtime.GOOS != "darwin" && m.run == nil {
		return errors.New("macos collector is darwin-only")
	}
	sample, err := m.sample(context.Background())
	if err != nil {
		return err
	}
	if sample.pressure == nil && sample.swapUsed == nil && sample.thermal == nil && sample.battery == nil {
		return errors.New("macos: no memory pressure, swap, thermal, or battery metrics")
	}
	add := func(id, title, units string, prio int, dims ...*registry.Dimension) {
		reg.AddChart(&registry.Chart{ID: id, Family: "macos", Title: title, Units: units, Priority: prio,
			Plugin: "macos", Module: "macos", Dimensions: dims})
	}
	if sample.pressure != nil {
		m.havePressure = true
		add("macos.memory_pressure", "Memory pressure", "percentage", 5000, &registry.Dimension{ID: "pressure"})
	}
	if sample.swapUsed != nil {
		m.haveSwap = true
		add("macos.swap", "Swap usage", "MiB", 5010, &registry.Dimension{ID: "used"}, &registry.Dimension{ID: "free"})
	}
	if sample.thermal != nil {
		m.haveThermal = true
		add("macos.thermal_level", "CPU thermal level", "level", 5020, &registry.Dimension{ID: "level"})
	}
	if sample.battery != nil {
		m.haveBattery = true
		add("macos.battery", "Battery charge", "percentage", 5030, &registry.Dimension{ID: "charge"})
	}
	return nil
}

func (m *macosCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	sample, err := m.sample(ctx)
	if err != nil {
		return err
	}
	if m.havePressure && sample.pressure != nil {
		_ = reg.Collect("macos.memory_pressure", now, map[string]float64{"pressure": *sample.pressure})
	}
	if m.haveSwap && sample.swapUsed != nil {
		vals := map[string]float64{"used": *sample.swapUsed}
		if sample.swapFree != nil {
			vals["free"] = *sample.swapFree
		}
		_ = reg.Collect("macos.swap", now, vals)
	}
	if m.haveThermal && sample.thermal != nil {
		_ = reg.Collect("macos.thermal_level", now, map[string]float64{"level": *sample.thermal})
	}
	if m.haveBattery && sample.battery != nil {
		_ = reg.Collect("macos.battery", now, map[string]float64{"charge": *sample.battery})
	}
	return nil
}

type macosSample struct {
	pressure, swapUsed, swapFree, thermal, battery *float64
}

func (m *macosCollector) sample(ctx context.Context) (macosSample, error) {
	var s macosSample
	sys, err := m.exec(ctx, m.cfg.Command, "-a")
	if err != nil && m.run == nil {
		return s, err
	}
	parseMacosSysctl(string(sys), &s)
	if mem, err := m.exec(ctx, "memory_pressure"); err == nil {
		parseMemoryPressure(string(mem), &s)
	}
	if batt, err := m.exec(ctx, "pmset", "-g", "batt"); err == nil {
		parsePmsetBatt(string(batt), &s)
	}
	if s.pressure == nil && s.swapUsed == nil && s.thermal == nil && s.battery == nil && err != nil {
		return s, err
	}
	return s, nil
}

func (m *macosCollector) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	if m.run != nil {
		return m.run(ctx, name, args...)
	}
	if _, err := exec.LookPath(name); err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
	defer cancel()
	return exec.CommandContext(cctx, name, args...).Output()
}

func parseMacosSysctl(s string, out *macosSample) {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			k, v, ok = strings.Cut(line, "=")
		}
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "kern.memorystatus_vm_pressure_level":
			if n, err := strconv.ParseFloat(v, 64); err == nil {
				// 0–4 → 0–100 so the chart stays a percentage.
				p := n * 25
				if p > 100 {
					p = 100
				}
				out.pressure = &p
			}
		case "machdep.xcpm.cpu_thermal_level":
			if n, err := strconv.ParseFloat(strings.Fields(v)[0], 64); err == nil {
				out.thermal = &n
			}
		case "vm.swapusage":
			used, free := parseSwapUsage(v)
			if used != nil {
				out.swapUsed = used
			}
			if free != nil {
				out.swapFree = free
			}
		}
	}
}

var swapField = regexp.MustCompile(`(used|free)\s*=\s*([0-9.]+)\s*([KMGT]?)`)

func parseSwapUsage(s string) (used, free *float64) {
	for _, m := range swapField.FindAllStringSubmatch(s, -1) {
		n, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			continue
		}
		switch strings.ToUpper(m[3]) {
		case "K":
			n /= 1024
		case "G":
			n *= 1024
		case "T":
			n *= 1024 * 1024
		case "":
			n /= 1024 * 1024 // bytes
		}
		switch m[1] {
		case "used":
			used = &n
		case "free":
			free = &n
		}
	}
	return used, free
}

var memFreePct = regexp.MustCompile(`(?i)memory free percentage:\s*([0-9.]+)\s*%`)

func parseMemoryPressure(s string, out *macosSample) {
	m := memFreePct.FindStringSubmatch(s)
	if m == nil {
		return
	}
	free, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return
	}
	p := 100 - free
	if p < 0 {
		p = 0
	}
	out.pressure = &p
}

var battPct = regexp.MustCompile(`(\d+(?:\.\d+)?)%`)

func parsePmsetBatt(s string, out *macosSample) {
	if !strings.Contains(strings.ToLower(s), "internalbattery") && !strings.Contains(s, "%") {
		return
	}
	m := battPct.FindStringSubmatch(s)
	if m == nil {
		return
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return
	}
	out.battery = &n
}
