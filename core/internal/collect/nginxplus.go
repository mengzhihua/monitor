package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// nginxplusConfig is collectors.modules.nginxplus (Plus API).
type nginxplusConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type nginxplusCollector struct {
	cfg    nginxplusConfig
	client *http.Client
	api    string
}

func init() {
	Register("nginxplus", func() Collector { return &nginxplusCollector{} })
}

func (n *nginxplusCollector) Name() string { return "nginxplus" }

func (n *nginxplusCollector) Configure(decode func(v any) error) error {
	if err := decode(&n.cfg); err != nil {
		return err
	}
	if n.cfg.Timeout <= 0 {
		n.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (n *nginxplusCollector) Init(reg *registry.Registry) error {
	if n.cfg.Timeout <= 0 {
		if err := n.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	n.client = &http.Client{Timeout: n.cfg.Timeout}
	base := strings.TrimRight(n.cfg.URL, "/")
	if base == "" {
		base = "http://127.0.0.1"
	}
	api, err := n.discover(context.Background(), base)
	if err != nil {
		return err
	}
	n.api = api
	if _, err := n.connections(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "nginxplus.client_connections_rate", Title: "Client connections rate", Units: "connections/s", Priority: 60000,
			Dimensions: []*registry.Dimension{{ID: "accepted", Algorithm: inc}, {ID: "dropped", Algorithm: inc}}},
		{ID: "nginxplus.client_connections_count", Title: "Client connections", Units: "connections", Priority: 60010,
			Dimensions: []*registry.Dimension{{ID: "active"}, {ID: "idle"}}},
		{ID: "nginxplus.http_requests_rate", Title: "HTTP requests rate", Units: "requests/s", Priority: 60020,
			Dimensions: []*registry.Dimension{{ID: "requests", Algorithm: inc}}},
		{ID: "nginxplus.http_requests_count", Title: "HTTP requests", Units: "requests", Priority: 60030,
			Dimensions: []*registry.Dimension{{ID: "current"}}},
		{ID: "nginxplus.ssl_handshakes_rate", Title: "SSL handshakes", Units: "handshakes/s", Priority: 60040,
			Dimensions: []*registry.Dimension{{ID: "handshakes", Algorithm: inc}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "nginxplus", "nginxplus", "nginxplus"
		reg.AddChart(ch)
	}
	return nil
}

func (n *nginxplusCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	c, err := n.connections(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("nginxplus.client_connections_rate", now, map[string]float64{"accepted": nestFloat(c, "accepted"), "dropped": nestFloat(c, "dropped")})
	_ = reg.Collect("nginxplus.client_connections_count", now, map[string]float64{"active": nestFloat(c, "active"), "idle": nestFloat(c, "idle")})
	if req, err := n.getMap(ctx, "/http/requests"); err == nil {
		_ = reg.Collect("nginxplus.http_requests_rate", now, map[string]float64{"requests": nestFloat(req, "total")})
		_ = reg.Collect("nginxplus.http_requests_count", now, map[string]float64{"current": nestFloat(req, "current")})
	}
	if ssl, err := n.getMap(ctx, "/ssl"); err == nil {
		_ = reg.Collect("nginxplus.ssl_handshakes_rate", now, map[string]float64{"handshakes": nestFloat(ssl, "handshakes")})
	}
	return nil
}

func (n *nginxplusCollector) discover(ctx context.Context, base string) (string, error) {
	b, err := httpGet(ctx, n.client, base+"/api/")
	if err != nil {
		return "", fmt.Errorf("nginxplus: %w", err)
	}
	s := strings.TrimSpace(string(b))
	ver := strings.Trim(s, "[] \n\"")
	if i := strings.LastIndex(ver, `"`); i >= 0 {
		parts := strings.Split(s, `"`)
		if len(parts) >= 2 {
			ver = parts[len(parts)-2]
		}
	}
	if ver == "" {
		ver = "7"
	}
	// pick last advertised version number
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '[' || r == ']' || r == '"' || r == ' ' || r == '\n' }) {
		if p != "" {
			ver = p
		}
	}
	return base + "/api/" + ver, nil
}

func (n *nginxplusCollector) connections(ctx context.Context) (map[string]any, error) {
	m, err := n.getMap(ctx, "/connections")
	if err != nil {
		return nil, fmt.Errorf("nginxplus: %w", err)
	}
	if _, ok := m["accepted"]; !ok && nestFloat(m, "active") == 0 {
		return nil, fmt.Errorf("nginxplus: no connections")
	}
	return m, nil
}

func (n *nginxplusCollector) getMap(ctx context.Context, path string) (map[string]any, error) {
	b, err := httpGet(ctx, n.client, n.api+path)
	if err != nil {
		return nil, err
	}
	return jsonMap(b)
}
