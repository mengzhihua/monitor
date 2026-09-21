package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// consulConfig is collectors.modules.consul (Agent HTTP API).
type consulConfig struct {
	URL     string        `yaml:"url"`
	Token   string        `yaml:"token"`
	Timeout time.Duration `yaml:"timeout"`
}

type consulCollector struct {
	cfg    consulConfig
	client *http.Client
}

func init() {
	Register("consul", func() Collector { return &consulCollector{} })
}

func (c *consulCollector) Name() string { return "consul" }

func (c *consulCollector) Configure(decode func(v any) error) error {
	if err := decode(&c.cfg); err != nil {
		return err
	}
	if c.cfg.URL == "" {
		c.cfg.URL = "http://127.0.0.1:8500"
	}
	if c.cfg.Timeout <= 0 {
		c.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (c *consulCollector) Init(reg *registry.Registry) error {
	if c.cfg.URL == "" {
		if err := c.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	c.client = &http.Client{Timeout: c.cfg.Timeout}
	if _, _, err := c.fetch(context.Background()); err != nil {
		return err
	}
	for _, ch := range []*registry.Chart{
		{ID: "consul.health_checks", Title: "Consul health checks", Units: "checks", Type: registry.Stacked, Priority: 44000,
			Dimensions: []*registry.Dimension{{ID: "passing"}, {ID: "warning"}, {ID: "critical"}}},
		{ID: "consul.autopilot", Title: "Consul autopilot", Units: "boolean", Priority: 44010,
			Dimensions: []*registry.Dimension{{ID: "healthy"}}},
		{ID: "consul.raft_peers", Title: "Consul raft peers", Units: "peers", Priority: 44020,
			Dimensions: []*registry.Dimension{{ID: "peers"}}},
		{ID: "consul.runtime_alloc", Title: "Consul runtime alloc", Units: "MiB", Priority: 44030,
			Dimensions: []*registry.Dimension{{ID: "alloc", Divisor: 1 << 20}}},
		{ID: "consul.serf_lan_members", Title: "Consul serf LAN members", Units: "members", Priority: 44040,
			Dimensions: []*registry.Dimension{{ID: "members"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "consul", "consul", "consul"
		reg.AddChart(ch)
	}
	return nil
}

type consulCheck struct {
	Status string `json:"Status"`
}

type consulMetrics struct {
	Gauges []struct {
		Name  string  `json:"Name"`
		Value float64 `json:"Value"`
	} `json:"Gauges"`
}

func (c *consulCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	checks, metrics, err := c.fetch(ctx)
	if err != nil {
		return err
	}
	var pass, warn, crit float64
	for _, ch := range checks {
		switch strings.ToLower(ch.Status) {
		case "passing":
			pass++
		case "warning":
			warn++
		default:
			crit++
		}
	}
	_ = reg.Collect("consul.health_checks", now, map[string]float64{"passing": pass, "warning": warn, "critical": crit})
	gauge := map[string]float64{}
	for _, g := range metrics.Gauges {
		gauge[g.Name] = g.Value
	}
	healthy := gauge["consul.autopilot.healthy"]
	_ = reg.Collect("consul.autopilot", now, map[string]float64{"healthy": healthy})
	_ = reg.Collect("consul.raft_peers", now, map[string]float64{"peers": gauge["consul.raft.peers"]})
	_ = reg.Collect("consul.runtime_alloc", now, map[string]float64{"alloc": gauge["consul.runtime.alloc_bytes"]})
	_ = reg.Collect("consul.serf_lan_members", now, map[string]float64{"members": gauge["consul.serf.lan.members"]})
	return nil
}

func (c *consulCollector) fetch(ctx context.Context) ([]consulCheck, consulMetrics, error) {
	var checks []consulCheck
	var metrics consulMetrics
	if err := c.getJSON(ctx, "/v1/health/state/any", &checks); err != nil {
		return nil, metrics, err
	}
	_ = c.getJSON(ctx, "/v1/agent/metrics", &metrics) // optional on tiny agents
	return checks, metrics, nil
}

func (c *consulCollector) getJSON(ctx context.Context, path string, dest any) error {
	url := strings.TrimRight(c.cfg.URL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if c.cfg.Token != "" {
		req.Header.Set("X-Consul-Token", c.cfg.Token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("consul %s: HTTP %d", path, resp.StatusCode)
	}
	return json.Unmarshal(body, dest)
}
