package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// maxscaleConfig is collectors.modules.maxscale (REST :8989).
type maxscaleConfig struct {
	URL      string        `yaml:"url"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type maxscaleCollector struct {
	cfg    maxscaleConfig
	client *http.Client
	url    string
}

func init() {
	Register("maxscale", func() Collector { return &maxscaleCollector{} })
}

func (m *maxscaleCollector) Name() string { return "maxscale" }

func (m *maxscaleCollector) Configure(decode func(v any) error) error {
	if err := decode(&m.cfg); err != nil {
		return err
	}
	if m.cfg.Timeout <= 0 {
		m.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (m *maxscaleCollector) Init(reg *registry.Registry) error {
	if m.cfg.Timeout <= 0 {
		if err := m.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	m.client = &http.Client{Timeout: m.cfg.Timeout}
	base := strings.TrimRight(m.cfg.URL, "/")
	if base == "" {
		base = "http://127.0.0.1:8989"
	}
	m.url = base
	if _, err := m.maxscale(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "maxscale.current_sessions", Title: "Current Sessions", Units: "sessions", Priority: 59900,
			Dimensions: []*registry.Dimension{{ID: "sessions"}}},
		{ID: "maxscale.poll_events", Title: "Poll Events", Units: "events/s", Priority: 59910,
			Dimensions: []*registry.Dimension{
				{ID: "reads", Algorithm: inc}, {ID: "writes", Algorithm: inc}, {ID: "accepts", Algorithm: inc},
				{ID: "errors", Algorithm: inc}, {ID: "hangups", Algorithm: inc},
			}},
		{ID: "maxscale.uptime", Title: "Uptime", Units: "seconds", Priority: 59920,
			Dimensions: []*registry.Dimension{{ID: "uptime"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "maxscale", "maxscale", "maxscale"
		reg.AddChart(ch)
	}
	return nil
}

func (m *maxscaleCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	st, err := m.maxscale(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("maxscale.uptime", now, map[string]float64{"uptime": nestFloat(st, "data", "attributes", "uptime")})
	th, err := m.threads(ctx)
	if err == nil {
		_ = reg.Collect("maxscale.current_sessions", now, map[string]float64{"sessions": nestFloat(th, "sessions")})
		_ = reg.Collect("maxscale.poll_events", now, map[string]float64{
			"reads": nestFloat(th, "reads"), "writes": nestFloat(th, "writes"), "accepts": nestFloat(th, "accepts"),
			"errors": nestFloat(th, "errors"), "hangups": nestFloat(th, "hangups"),
		})
	}
	return nil
}

func (m *maxscaleCollector) getJSON(ctx context.Context, path string) (map[string]any, error) {
	b, err := httpGetAuth(ctx, m.client, m.url+path, m.cfg.User, m.cfg.Password)
	if err != nil {
		return nil, err
	}
	return jsonMap(b)
}

func (m *maxscaleCollector) maxscale(ctx context.Context) (map[string]any, error) {
	st, err := m.getJSON(ctx, "/v1/maxscale")
	if err != nil {
		return nil, fmt.Errorf("maxscale: %w", err)
	}
	if nestMap(st, "data") == nil && nestFloat(st, "uptime") == 0 {
		return nil, fmt.Errorf("maxscale: unexpected response")
	}
	return st, nil
}

func (m *maxscaleCollector) threads(ctx context.Context) (map[string]float64, error) {
	st, err := m.getJSON(ctx, "/v1/maxscale/threads")
	if err != nil {
		return nil, err
	}
	out := map[string]float64{}
	for _, item := range nestSlice(st, "data") {
		mm, _ := item.(map[string]any)
		attr := nestMap(mm, "attributes")
		stats := nestMap(attr, "stats")
		if stats == nil {
			stats = attr
		}
		out["sessions"] += nestFloat(stats, "sessions")
		out["reads"] += nestFloat(stats, "reads")
		out["writes"] += nestFloat(stats, "writes")
		out["accepts"] += nestFloat(stats, "accepts")
		out["errors"] += nestFloat(stats, "errors")
		out["hangups"] += nestFloat(stats, "hangups")
	}
	return out, nil
}
