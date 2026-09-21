package collect

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// hpssaConfig is collectors.modules.hpssa (`ssacli ctrl all show config`).
type hpssaConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type hpssaCollector struct {
	cfg  hpssaConfig
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	seen map[string]bool
}

func init() {
	Register("hpssa", func() Collector { return &hpssaCollector{} })
}

func (h *hpssaCollector) Name() string { return "hpssa" }

func (h *hpssaCollector) Configure(decode func(v any) error) error {
	if err := decode(&h.cfg); err != nil {
		return err
	}
	if h.cfg.Command == "" {
		h.cfg.Command = "ssacli"
	}
	if h.cfg.Timeout <= 0 {
		h.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (h *hpssaCollector) Init(reg *registry.Registry) error {
	if h.cfg.Command == "" {
		if err := h.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if h.run == nil {
		h.run = execRun(h.cfg.Timeout)
	}
	h.seen = map[string]bool{}
	ctrls, err := h.controllers(context.Background())
	if err != nil {
		return err
	}
	if len(ctrls) == 0 {
		return fmt.Errorf("hpssa: no controllers")
	}
	return nil
}

func (h *hpssaCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	ctrls, err := h.controllers(ctx)
	if err != nil {
		return err
	}
	for _, c := range ctrls {
		id := sanitizeID(c.model + "_slot_" + c.slot)
		if !h.seen[id] {
			h.seen[id] = true
			ch := &registry.Chart{ID: "hpssa.controller_status." + id, Context: "hpssa.controller_status",
				Title: "Controller status", Units: "status", Priority: 57300,
				Dimensions: []*registry.Dimension{{ID: "ok"}, {ID: "nok"}}}
			ch.Family, ch.Plugin, ch.Module = "hpssa", "hpssa", "hpssa"
			reg.AddChart(ch)
		}
		ok := 1.0
		if !strings.EqualFold(c.status, "OK") {
			ok = 0
		}
		_ = reg.Collect("hpssa.controller_status."+id, now, map[string]float64{"ok": ok, "nok": 1 - ok})
	}
	return nil
}

type hpssaCtrl struct{ model, slot, status string }

func (h *hpssaCollector) controllers(ctx context.Context) ([]hpssaCtrl, error) {
	b, err := h.run(ctx, h.cfg.Command, "ctrl", "all", "show", "config")
	if err != nil {
		b, err = h.run(ctx, h.cfg.Command, "ctrl", "all", "show", "config", "detail")
		if err != nil {
			return nil, fmt.Errorf("hpssa: %w", err)
		}
	}
	return parseHPSSA(string(b)), nil
}

func parseHPSSA(s string) []hpssaCtrl {
	var out []hpssaCtrl
	var cur *hpssaCtrl
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if strings.Contains(t, " in Slot ") {
			if cur != nil {
				out = append(out, *cur)
			}
			model, slot, _ := strings.Cut(t, " in Slot ")
			fields := strings.Fields(slot)
			if len(fields) == 0 {
				continue
			}
			cur = &hpssaCtrl{model: strings.TrimSpace(model), slot: fields[0], status: "OK"}
			continue
		}
		if cur != nil && strings.HasPrefix(t, "Controller Status:") {
			cur.status = colonVal(t)
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}
