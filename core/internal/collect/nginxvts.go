package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// nginxvtsConfig is collectors.modules.nginxvts (vhost_traffic_status JSON).
type nginxvtsConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type nginxvtsCollector struct {
	cfg    nginxvtsConfig
	client *http.Client
	url    string
}

func init() {
	Register("nginxvts", func() Collector { return &nginxvtsCollector{} })
}

func (n *nginxvtsCollector) Name() string { return "nginxvts" }

func (n *nginxvtsCollector) Configure(decode func(v any) error) error {
	if err := decode(&n.cfg); err != nil {
		return err
	}
	if n.cfg.Timeout <= 0 {
		n.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (n *nginxvtsCollector) Init(reg *registry.Registry) error {
	if n.cfg.Timeout <= 0 {
		if err := n.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	n.client = &http.Client{Timeout: n.cfg.Timeout}
	base := strings.TrimRight(n.cfg.URL, "/")
	if base == "" {
		base = "http://127.0.0.1/status/format/json"
	}
	n.url = base
	if _, err := n.metrics(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "nginxvts.requests_total", Title: "Total requests", Units: "requests/s", Priority: 58500,
			Dimensions: []*registry.Dimension{{ID: "requests", Algorithm: inc}}},
		{ID: "nginxvts.active_connections", Title: "Active connections", Units: "connections", Priority: 58510,
			Dimensions: []*registry.Dimension{{ID: "active"}}},
		{ID: "nginxvts.connections_total", Title: "Total connections", Units: "connections/s", Priority: 58520,
			Dimensions: []*registry.Dimension{
				{ID: "reading", Algorithm: inc}, {ID: "writing", Algorithm: inc}, {ID: "waiting", Algorithm: inc},
				{ID: "accepted", Algorithm: inc}, {ID: "handled", Algorithm: inc},
			}},
		{ID: "nginxvts.uptime", Title: "Uptime", Units: "seconds", Priority: 58530,
			Dimensions: []*registry.Dimension{{ID: "uptime"}}},
		{ID: "nginxvts.shm_usage", Title: "Shared memory size", Units: "bytes", Priority: 58540,
			Dimensions: []*registry.Dimension{{ID: "max"}, {ID: "used"}}},
		{ID: "nginxvts.server_responses_total", Title: "Total number of responses by code class", Units: "responses/s", Priority: 58550,
			Dimensions: []*registry.Dimension{
				{ID: "1xx", Algorithm: inc}, {ID: "2xx", Algorithm: inc}, {ID: "3xx", Algorithm: inc},
				{ID: "4xx", Algorithm: inc}, {ID: "5xx", Algorithm: inc},
			}},
		{ID: "nginxvts.server_traffic_total", Title: "Total amount of data transferred to and from the server", Units: "bytes/s", Priority: 58560,
			Dimensions: []*registry.Dimension{{ID: "in", Algorithm: inc}, {ID: "out", Algorithm: inc}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "nginxvts", "nginxvts", "nginxvts"
		reg.AddChart(ch)
	}
	return nil
}

func (n *nginxvtsCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := n.metrics(ctx)
	if err != nil {
		return err
	}
	conn := nestMap(m, "connections")
	up := (nestFloat(m, "nowMsec") - nestFloat(m, "loadMsec")) / 1000
	_ = reg.Collect("nginxvts.requests_total", now, map[string]float64{"requests": nestFloat(conn, "requests")})
	_ = reg.Collect("nginxvts.active_connections", now, map[string]float64{"active": nestFloat(conn, "active")})
	_ = reg.Collect("nginxvts.connections_total", now, map[string]float64{
		"reading": nestFloat(conn, "reading"), "writing": nestFloat(conn, "writing"), "waiting": nestFloat(conn, "waiting"),
		"accepted": nestFloat(conn, "accepted"), "handled": nestFloat(conn, "handled"),
	})
	_ = reg.Collect("nginxvts.uptime", now, map[string]float64{"uptime": up})
	shm := nestMap(m, "sharedZones")
	_ = reg.Collect("nginxvts.shm_usage", now, map[string]float64{"max": nestFloat(shm, "maxSize"), "used": nestFloat(shm, "usedSize")})
	zones := nestMap(m, "serverZones")
	star := nestMap(zones, "*")
	resp := nestMap(star, "responses")
	_ = reg.Collect("nginxvts.server_responses_total", now, map[string]float64{
		"1xx": nestFloat(resp, "1xx"), "2xx": nestFloat(resp, "2xx"), "3xx": nestFloat(resp, "3xx"),
		"4xx": nestFloat(resp, "4xx"), "5xx": nestFloat(resp, "5xx"),
	})
	_ = reg.Collect("nginxvts.server_traffic_total", now, map[string]float64{"in": nestFloat(star, "inBytes"), "out": nestFloat(star, "outBytes")})
	return nil
}

func (n *nginxvtsCollector) metrics(ctx context.Context) (map[string]any, error) {
	b, err := httpGet(ctx, n.client, n.url)
	if err != nil {
		return nil, fmt.Errorf("nginxvts: %w", err)
	}
	m, err := jsonMap(b)
	if err != nil {
		return nil, fmt.Errorf("nginxvts: %w", err)
	}
	if nestMap(m, "connections") == nil {
		return nil, fmt.Errorf("nginxvts: no connections")
	}
	return m, nil
}
