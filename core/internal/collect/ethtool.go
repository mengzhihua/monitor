package collect

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// ethtoolConfig is collectors.modules.ethtool (optical DDM via `ethtool -m`).
type ethtoolConfig struct {
	Command           string        `yaml:"command"`
	OpticalInterfaces string        `yaml:"optical_interfaces"`
	Timeout           time.Duration `yaml:"timeout"`
}

type ethtoolDDM struct {
	rx, tx, bias, temp, volt                float64
	hasRx, hasTx, hasBias, hasTemp, hasVolt bool
}

type ethtoolCollector struct {
	cfg  ethtoolConfig
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	seen map[string]bool
}

func init() {
	Register("ethtool", func() Collector { return &ethtoolCollector{} })
}

func (e *ethtoolCollector) Name() string { return "ethtool" }

func (e *ethtoolCollector) Configure(decode func(v any) error) error {
	if err := decode(&e.cfg); err != nil {
		return err
	}
	if e.cfg.Command == "" {
		e.cfg.Command = "ethtool"
	}
	if e.cfg.Timeout <= 0 {
		e.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (e *ethtoolCollector) Init(reg *registry.Registry) error {
	if e.cfg.Command == "" {
		if err := e.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if e.run == nil {
		e.run = execRun(e.cfg.Timeout)
	}
	e.seen = map[string]bool{}
	ifaces := strings.Fields(e.cfg.OpticalInterfaces)
	if len(ifaces) == 0 {
		return fmt.Errorf("ethtool: no optical interfaces")
	}
	ok := 0
	var last error
	for _, iface := range ifaces {
		if _, err := e.module(context.Background(), iface); err != nil {
			last = err
			continue
		}
		ok++
	}
	if ok == 0 {
		if last == nil {
			last = fmt.Errorf("no ddm")
		}
		return fmt.Errorf("ethtool: %w", last)
	}
	return nil
}

func (e *ethtoolCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	ok := 0
	var last error
	for _, iface := range strings.Fields(e.cfg.OpticalInterfaces) {
		ddm, err := e.module(ctx, iface)
		if err != nil {
			last = err
			continue
		}
		id := sanitizeID(iface)
		if !e.seen[id] {
			e.seen[id] = true
			e.addCharts(reg, id)
		}
		if ddm.hasRx {
			_ = reg.Collect("ethtool.optical_module_receiver_signal_power."+id, now, map[string]float64{"rx_power": ddm.rx})
		}
		if ddm.hasTx {
			_ = reg.Collect("ethtool.optical_module_laser_output_power."+id, now, map[string]float64{"tx_power": ddm.tx})
		}
		if ddm.hasBias {
			_ = reg.Collect("ethtool.optical_module_laser_bias_current."+id, now, map[string]float64{"bias_current": ddm.bias})
		}
		if ddm.hasTemp {
			_ = reg.Collect("ethtool.optical_module_temperature."+id, now, map[string]float64{"temperature": ddm.temp})
		}
		if ddm.hasVolt {
			_ = reg.Collect("ethtool.optical_module_voltage."+id, now, map[string]float64{"voltage": ddm.volt})
		}
		ok++
	}
	if ok == 0 && last != nil {
		return last
	}
	return nil
}

func (e *ethtoolCollector) addCharts(reg *registry.Registry, id string) {
	for _, ch := range []*registry.Chart{
		{ID: "ethtool.optical_module_receiver_signal_power." + id, Context: "ethtool.optical_module_receiver_signal_power", Title: "Module Receiver Signal Average Optical Power", Units: "dBm", Priority: 61000,
			Dimensions: []*registry.Dimension{{ID: "rx_power"}}},
		{ID: "ethtool.optical_module_laser_output_power." + id, Context: "ethtool.optical_module_laser_output_power", Title: "Module Laser Output Power", Units: "dBm", Priority: 61010,
			Dimensions: []*registry.Dimension{{ID: "tx_power"}}},
		{ID: "ethtool.optical_module_laser_bias_current." + id, Context: "ethtool.optical_module_laser_bias_current", Title: "Module Laser Bias Current", Units: "mA", Priority: 61020,
			Dimensions: []*registry.Dimension{{ID: "bias_current"}}},
		{ID: "ethtool.optical_module_temperature." + id, Context: "ethtool.optical_module_temperature", Title: "Module Temperature", Units: "Celsius", Priority: 61030,
			Dimensions: []*registry.Dimension{{ID: "temperature"}}},
		{ID: "ethtool.optical_module_voltage." + id, Context: "ethtool.optical_module_voltage", Title: "Module Voltage", Units: "Volts", Priority: 61040,
			Dimensions: []*registry.Dimension{{ID: "voltage"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "ethtool", "ethtool", "ethtool"
		reg.AddChart(ch)
	}
}

func (e *ethtoolCollector) module(ctx context.Context, iface string) (ethtoolDDM, error) {
	b, err := e.run(ctx, e.cfg.Command, "-m", iface)
	if err != nil {
		return ethtoolDDM{}, fmt.Errorf("ethtool: %w", err)
	}
	ddm := parseEthtoolModule(string(b))
	if !ddm.hasRx && !ddm.hasTx && !ddm.hasTemp && !ddm.hasVolt && !ddm.hasBias {
		return ethtoolDDM{}, fmt.Errorf("ethtool: no ddm for %s", iface)
	}
	return ddm, nil
}

func parseEthtoolModule(s string) ethtoolDDM {
	var d ethtoolDDM
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		low := strings.ToLower(t)
		switch {
		case strings.Contains(low, "receiver signal average optical power") || strings.Contains(low, "laser rx power") || (strings.Contains(low, "rx power") && strings.Contains(low, "dbm")):
			d.rx, d.hasRx = ethtoolDBM(t), true
		case strings.Contains(low, "laser tx power") || strings.Contains(low, "laser output power") || (strings.Contains(low, "tx power") && strings.Contains(low, "dbm")):
			d.tx, d.hasTx = ethtoolDBM(t), true
		case strings.Contains(low, "laser tx bias") || strings.Contains(low, "laser bias"):
			d.bias, d.hasBias = firstFloat(t), true
		case strings.Contains(low, "module temperature") || strings.HasPrefix(low, "temperature"):
			d.temp, d.hasTemp = firstFloat(t), true
		case strings.Contains(low, "module voltage") || strings.Contains(low, "voltage"):
			d.volt, d.hasVolt = firstFloat(t), true
		}
	}
	return d
}

func ethtoolDBM(t string) float64 {
	if i := strings.LastIndex(t, "/"); i >= 0 {
		return firstFloat(t[i+1:])
	}
	return firstFloat(t)
}
