package collect

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// megacliConfig is collectors.modules.megacli (`megacli -LDPDInfo -aALL`).
type megacliConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type megacliCollector struct {
	cfg  megacliConfig
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	seen map[string]bool
}

func init() {
	Register("megacli", func() Collector { return &megacliCollector{} })
}

func (m *megacliCollector) Name() string { return "megacli" }

func (m *megacliCollector) Configure(decode func(v any) error) error {
	if err := decode(&m.cfg); err != nil {
		return err
	}
	if m.cfg.Command == "" {
		m.cfg.Command = "megacli"
	}
	if m.cfg.Timeout <= 0 {
		m.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (m *megacliCollector) Init(reg *registry.Registry) error {
	if m.cfg.Command == "" {
		if err := m.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if m.run == nil {
		m.run = execRun(m.cfg.Timeout)
	}
	m.seen = map[string]bool{}
	info, err := m.pdinfo(context.Background())
	if err != nil {
		return err
	}
	if len(parseMegaAdapters(info)) == 0 && len(parseMegaDrives(info)) == 0 {
		return fmt.Errorf("megacli: no adapters")
	}
	return nil
}

func (m *megacliCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	info, err := m.pdinfo(ctx)
	if err != nil {
		return err
	}
	for _, ad := range parseMegaAdapters(info) {
		id := sanitizeID(ad)
		m.ensureAdapter(reg, id)
		st := strings.ToLower(parseMegaAdapterState(info, ad))
		_ = reg.Collect("megacli.adapter_health_state."+id, now, map[string]float64{
			"optimal": bool01(st == "optimal"), "degraded": bool01(st == "degraded"),
			"partially_degraded": bool01(strings.Contains(st, "partial")), "failed": bool01(st == "failed"),
		})
	}
	for _, d := range parseMegaDrives(info) {
		id := sanitizeID(d.wwn)
		m.ensureDrive(reg, id)
		_ = reg.Collect("megacli.phys_drive_media_errors."+id, now, map[string]float64{"media_errors": d.media})
		_ = reg.Collect("megacli.phys_drive_predictive_failures."+id, now, map[string]float64{"predictive_failures": d.pred})
	}
	if bbu, err := m.bbu(ctx); err == nil {
		for ad, charge := range parseMegaBBU(bbu) {
			id := sanitizeID(ad)
			m.ensureBBU(reg, id)
			_ = reg.Collect("megacli.bbu_charge."+id, now, map[string]float64{"charge": charge})
		}
	}
	return nil
}

func (m *megacliCollector) pdinfo(ctx context.Context) (string, error) {
	b, err := m.run(ctx, m.cfg.Command, "-LDPDInfo", "-aALL", "-NoLog")
	if err != nil {
		return "", fmt.Errorf("megacli: %w", err)
	}
	return string(b), nil
}

func (m *megacliCollector) bbu(ctx context.Context) (string, error) {
	b, err := m.run(ctx, m.cfg.Command, "-AdpBbuCmd", "-aALL", "-NoLog")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (m *megacliCollector) ensureAdapter(reg *registry.Registry, id string) {
	key := "ad:" + id
	if m.seen[key] {
		return
	}
	m.seen[key] = true
	ch := &registry.Chart{ID: "megacli.adapter_health_state." + id, Context: "megacli.adapter_health_state",
		Title: "Adapter health state", Units: "state", Priority: 57200,
		Dimensions: []*registry.Dimension{{ID: "optimal"}, {ID: "degraded"}, {ID: "partially_degraded"}, {ID: "failed"}}}
	ch.Family, ch.Plugin, ch.Module = "megacli", "megacli", "megacli"
	reg.AddChart(ch)
}

func (m *megacliCollector) ensureDrive(reg *registry.Registry, id string) {
	key := "pd:" + id
	if m.seen[key] {
		return
	}
	m.seen[key] = true
	for _, ch := range []*registry.Chart{
		{ID: "megacli.phys_drive_media_errors." + id, Context: "megacli.phys_drive_media_errors", Title: "Physical Drive media errors rate", Units: "errors/s", Priority: 57210,
			Dimensions: []*registry.Dimension{{ID: "media_errors"}}},
		{ID: "megacli.phys_drive_predictive_failures." + id, Context: "megacli.phys_drive_predictive_failures", Title: "Physical Drive predictive failures rate", Units: "failures/s", Priority: 57220,
			Dimensions: []*registry.Dimension{{ID: "predictive_failures"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "megacli", "megacli", "megacli"
		reg.AddChart(ch)
	}
}

func (m *megacliCollector) ensureBBU(reg *registry.Registry, id string) {
	key := "bbu:" + id
	if m.seen[key] {
		return
	}
	m.seen[key] = true
	ch := &registry.Chart{ID: "megacli.bbu_charge." + id, Context: "megacli.bbu_charge", Title: "BBU relative charge", Units: "percentage", Type: registry.Area, Priority: 57230,
		Dimensions: []*registry.Dimension{{ID: "charge"}}}
	ch.Family, ch.Plugin, ch.Module = "megacli", "megacli", "megacli"
	reg.AddChart(ch)
}

func bool01(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

func colonVal(line string) string {
	_, after, ok := strings.Cut(line, ":")
	if !ok {
		return ""
	}
	return strings.TrimSpace(after)
}

func parseMegaAdapters(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Adapter #") || strings.HasPrefix(line, "Adapter:") {
			id := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "Adapter #"), "Adapter:"))
			id = strings.TrimSpace(strings.Split(id, " ")[0])
			if id != "" {
				out = append(out, id)
			}
		}
	}
	return out
}

func parseMegaAdapterState(s, adapter string) string {
	cur := ""
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "Adapter #") || strings.HasPrefix(t, "Adapter:") {
			cur = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(t, "Adapter #"), "Adapter:"))
			cur = strings.TrimSpace(strings.Split(cur, " ")[0])
		}
		if cur == adapter {
			low := strings.ToLower(t)
			if strings.HasPrefix(low, "state") {
				if v := colonVal(t); v != "" {
					return v
				}
			}
		}
	}
	return "optimal"
}

type megaDrive struct {
	wwn         string
	media, pred float64
}

func parseMegaDrives(s string) []megaDrive {
	var out []megaDrive
	cur := megaDrive{}
	flush := func() {
		if cur.wwn != "" {
			out = append(out, cur)
			cur = megaDrive{}
		}
	}
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "WWN"):
			flush()
			cur.wwn = strings.ReplaceAll(colonVal(t), " ", "")
		case strings.HasPrefix(t, "Media Error Count"):
			cur.media = firstFloat(colonVal(t))
		case strings.HasPrefix(t, "Predictive Failure Count"):
			cur.pred = firstFloat(colonVal(t))
		}
	}
	flush()
	return out
}

func parseMegaBBU(s string) map[string]float64 {
	out := map[string]float64{}
	ad := "0"
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.Contains(t, "Adapter:") || strings.Contains(t, "Adapter #") {
			ad = colonVal(t)
			if ad == "" {
				ad = strings.TrimSpace(strings.TrimPrefix(t, "BBU status for Adapter:"))
			}
			ad = strings.TrimSpace(strings.Split(ad, " ")[0])
		}
		if strings.Contains(t, "Relative State of Charge") {
			out[ad] = firstFloat(colonVal(t))
		}
	}
	return out
}
