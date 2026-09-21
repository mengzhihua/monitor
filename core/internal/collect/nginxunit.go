package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// nginxunitConfig is collectors.modules.nginxunit (status JSON).
type nginxunitConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type nginxunitCollector struct {
	cfg    nginxunitConfig
	client *http.Client
	url    string
}

func init() {
	Register("nginxunit", func() Collector { return &nginxunitCollector{} })
}

func (n *nginxunitCollector) Name() string { return "nginxunit" }

func (n *nginxunitCollector) Configure(decode func(v any) error) error {
	if err := decode(&n.cfg); err != nil {
		return err
	}
	if n.cfg.Timeout <= 0 {
		n.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (n *nginxunitCollector) Init(reg *registry.Registry) error {
	if n.cfg.Timeout <= 0 {
		if err := n.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	n.client = &http.Client{Timeout: n.cfg.Timeout}
	base := strings.TrimRight(n.cfg.URL, "/")
	if base == "" {
		base = "http://127.0.0.1:8000"
	}
	n.url = base
	if _, err := n.status(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "nginxunit.requests_rate", Title: "Requests", Units: "requests/s", Priority: 60100,
			Dimensions: []*registry.Dimension{{ID: "requests", Algorithm: inc}}},
		{ID: "nginxunit.connections_rate", Title: "Connections", Units: "connections/s", Type: registry.Stacked, Priority: 60110,
			Dimensions: []*registry.Dimension{{ID: "accepted", Algorithm: inc}, {ID: "closed", Algorithm: inc}}},
		{ID: "nginxunit.connections_current", Title: "Current Connections", Units: "connections", Type: registry.Stacked, Priority: 60120,
			Dimensions: []*registry.Dimension{{ID: "active"}, {ID: "idle"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "nginxunit", "nginxunit", "nginxunit"
		reg.AddChart(ch)
	}
	return nil
}

func (n *nginxunitCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := n.status(ctx)
	if err != nil {
		return err
	}
	req := nestMap(m, "requests")
	conn := nestMap(m, "connections")
	_ = reg.Collect("nginxunit.requests_rate", now, map[string]float64{"requests": nestFloat(req, "total")})
	_ = reg.Collect("nginxunit.connections_rate", now, map[string]float64{"accepted": nestFloat(conn, "accepted"), "closed": nestFloat(conn, "closed")})
	_ = reg.Collect("nginxunit.connections_current", now, map[string]float64{"active": nestFloat(conn, "active"), "idle": nestFloat(conn, "idle")})
	return nil
}

func (n *nginxunitCollector) status(ctx context.Context) (map[string]any, error) {
	b, err := httpGet(ctx, n.client, n.url+"/status")
	if err != nil {
		b, err = httpGet(ctx, n.client, n.url)
		if err != nil {
			return nil, fmt.Errorf("nginxunit: %w", err)
		}
	}
	m, err := jsonMap(b)
	if err != nil {
		return nil, fmt.Errorf("nginxunit: %w", err)
	}
	if nestMap(m, "connections") == nil && nestMap(m, "requests") == nil {
		return nil, fmt.Errorf("nginxunit: no status")
	}
	return m, nil
}
