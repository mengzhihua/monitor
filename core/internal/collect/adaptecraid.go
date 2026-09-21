package collect

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// adaptecraidConfig is collectors.modules.adaptecraid (`arcconf getconfig 1`).
type adaptecraidConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type adaptecraidCollector struct {
	cfg  adaptecraidConfig
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	seen map[string]bool
}

func init() {
	Register("adaptecraid", func() Collector { return &adaptecraidCollector{} })
}

func (a *adaptecraidCollector) Name() string { return "adaptecraid" }

func (a *adaptecraidCollector) Configure(decode func(v any) error) error {
	if err := decode(&a.cfg); err != nil {
		return err
	}
	if a.cfg.Command == "" {
		a.cfg.Command = "arcconf"
	}
	if a.cfg.Timeout <= 0 {
		a.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (a *adaptecraidCollector) Init(reg *registry.Registry) error {
	if a.cfg.Command == "" {
		if err := a.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if a.run == nil {
		a.run = execRun(a.cfg.Timeout)
	}
	a.seen = map[string]bool{}
	lds, pds, err := a.config(context.Background())
	if err != nil {
		return err
	}
	if len(lds) == 0 && len(pds) == 0 {
		return fmt.Errorf("adaptecraid: no devices")
	}
	return nil
}

func (a *adaptecraidCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	lds, pds, err := a.config(ctx)
	if err != nil {
		return err
	}
	for _, ld := range lds {
		id := sanitizeID(ld.num)
		if !a.seen["ld:"+id] {
			a.seen["ld:"+id] = true
			ch := &registry.Chart{ID: "adaptecraid.logical_device_status." + id, Context: "adaptecraid.logical_device_status",
				Title: "Logical Device status", Units: "status", Priority: 57400,
				Dimensions: []*registry.Dimension{{ID: "ok"}, {ID: "critical"}}}
			ch.Family, ch.Plugin, ch.Module = "adaptecraid", "adaptecraid", "adaptecraid"
			reg.AddChart(ch)
		}
		ok := bool01(strings.EqualFold(ld.status, "Optimal") || strings.EqualFold(ld.status, "Okay") || strings.EqualFold(ld.status, "OK"))
		_ = reg.Collect("adaptecraid.logical_device_status."+id, now, map[string]float64{"ok": ok, "critical": 1 - ok})
	}
	for _, pd := range pds {
		id := sanitizeID(pd.num)
		if !a.seen["pd:"+id] {
			a.seen["pd:"+id] = true
			ch := &registry.Chart{ID: "adaptecraid.physical_device_state." + id, Context: "adaptecraid.physical_device_state",
				Title: "Physical Device state", Units: "state", Priority: 57410,
				Dimensions: []*registry.Dimension{{ID: "ok"}, {ID: "critical"}}}
			ch.Family, ch.Plugin, ch.Module = "adaptecraid", "adaptecraid", "adaptecraid"
			reg.AddChart(ch)
		}
		ok := bool01(strings.EqualFold(pd.state, "Online") || strings.EqualFold(pd.state, "Ready") || strings.EqualFold(pd.state, "Optimal"))
		_ = reg.Collect("adaptecraid.physical_device_state."+id, now, map[string]float64{"ok": ok, "critical": 1 - ok})
	}
	return nil
}

type arcLD struct{ num, status string }
type arcPD struct{ num, state string }

func (a *adaptecraidCollector) config(ctx context.Context) ([]arcLD, []arcPD, error) {
	b, err := a.run(ctx, a.cfg.Command, "GETCONFIG", "1")
	if err != nil {
		b, err = a.run(ctx, a.cfg.Command, "getconfig", "1")
		if err != nil {
			return nil, nil, fmt.Errorf("adaptecraid: %w", err)
		}
	}
	lds, pds := parseArcconf(string(b))
	return lds, pds, nil
}

func parseArcconf(s string) ([]arcLD, []arcPD) {
	var lds []arcLD
	var pds []arcPD
	var ld *arcLD
	var pd *arcPD
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "Logical device number") {
			if ld != nil {
				lds = append(lds, *ld)
			}
			ld = &arcLD{num: strings.TrimSpace(strings.TrimPrefix(t, "Logical device number")), status: "Optimal"}
			pd = nil
			continue
		}
		if strings.HasPrefix(t, "Device #") {
			if ld != nil {
				lds = append(lds, *ld)
				ld = nil
			}
			if pd != nil {
				pds = append(pds, *pd)
			}
			pd = &arcPD{num: strings.TrimSpace(strings.TrimPrefix(t, "Device #")), state: "Online"}
			continue
		}
		if ld != nil && strings.HasPrefix(t, "Status of logical device") {
			ld.status = colonVal(t)
		}
		if pd != nil && strings.HasPrefix(t, "State") {
			pd.state = colonVal(t)
		}
	}
	if ld != nil {
		lds = append(lds, *ld)
	}
	if pd != nil {
		pds = append(pds, *pd)
	}
	return lds, pds
}
