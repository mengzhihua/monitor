package collect

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// ipmiConfig is collectors.modules.ipmi (Netdata freeipmi.plugin via ipmitool sdr).
type ipmiConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type ipmiCollector struct {
	cfg  ipmiConfig
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	seen map[string]bool
}

func init() {
	Register("ipmi", func() Collector { return &ipmiCollector{} })
}

func (i *ipmiCollector) Name() string { return "ipmi" }

func (i *ipmiCollector) Configure(decode func(v any) error) error {
	if err := decode(&i.cfg); err != nil {
		return err
	}
	if i.cfg.Command == "" {
		i.cfg.Command = "ipmitool"
	}
	if i.cfg.Timeout <= 0 {
		i.cfg.Timeout = 8 * time.Second
	}
	return nil
}

func (i *ipmiCollector) Init(reg *registry.Registry) error {
	if i.cfg.Command == "" {
		if err := i.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if i.run == nil {
		i.run = execRun(i.cfg.Timeout)
	}
	raw, err := i.run(context.Background(), i.cfg.Command, "sdr")
	if err != nil {
		return fmt.Errorf("ipmi: ipmitool unavailable: %w", err)
	}
	if len(parseIPMISDR(string(raw))) == 0 {
		return fmt.Errorf("ipmi: no SDR sensors")
	}
	i.seen = map[string]bool{}
	st := sysChart("ipmi.sensor_state", "ipmi", "IPMI sensor states", "sensors", 39000,
		&registry.Dimension{ID: "ok"}, &registry.Dimension{ID: "warning"}, &registry.Dimension{ID: "critical"},
		&registry.Dimension{ID: "ns"})
	st.Plugin, st.Module, st.Family = "ipmi", "ipmi", "ipmi"
	reg.AddChart(st)
	return nil
}

func (i *ipmiCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	raw, err := i.run(ctx, i.cfg.Command, "sdr")
	if err != nil {
		return err
	}
	okc, warn, crit, ns := 0.0, 0.0, 0.0, 0.0
	for _, s := range parseIPMISDR(string(raw)) {
		switch s.State {
		case "ok":
			okc++
		case "nc", "warning", "non-critical":
			warn++
		case "cr", "critical", "nr", "non-recoverable":
			crit++
		default:
			ns++
		}
		if s.Kind == "" || s.State == "ns" || s.State == "no reading" {
			continue
		}
		id := "ipmi." + s.Kind + "." + sanitizeID(s.Name)
		if !i.seen[id] {
			i.seen[id] = true
			ch := sysChart(id, "ipmi", "IPMI "+s.Name, s.Units, 39010, &registry.Dimension{ID: "value"})
			ch.Plugin, ch.Module, ch.Context = "ipmi", "ipmi", "ipmi."+s.Kind
			reg.AddChart(ch)
		}
		_ = reg.Collect(id, now, map[string]float64{"value": s.Value})
	}
	_ = reg.Collect("ipmi.sensor_state", now, map[string]float64{"ok": okc, "warning": warn, "critical": crit, "ns": ns})
	return nil
}

type ipmiSensor struct {
	Name, State, Kind, Units string
	Value                    float64
}

func parseIPMISDR(s string) []ipmiSensor {
	var out []ipmiSensor
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.Contains(line, "|") {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 3 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		reading := strings.TrimSpace(parts[1])
		state := strings.ToLower(strings.TrimSpace(parts[2]))
		kind, units := "", ""
		low := strings.ToLower(reading)
		switch {
		case strings.Contains(low, "degrees") || strings.Contains(low, "degree"):
			kind, units = "temperatures", "Celsius"
		case strings.Contains(low, "rpm"):
			kind, units = "fans", "RPM"
		case strings.Contains(low, "volt"):
			kind, units = "voltage", "Volts"
		case strings.Contains(low, "watt"):
			kind, units = "power", "Watts"
		}
		out = append(out, ipmiSensor{Name: name, State: state, Kind: kind, Units: units, Value: firstFloat(reading)})
	}
	return out
}
