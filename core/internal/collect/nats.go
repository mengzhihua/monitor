package collect

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// natsConfig is collectors.modules.nats (monitoring /varz).
type natsConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type natsCollector struct {
	cfg    natsConfig
	client *http.Client
}

func init() {
	Register("nats", func() Collector { return &natsCollector{} })
}

func (n *natsCollector) Name() string { return "nats" }

func (n *natsCollector) Configure(decode func(v any) error) error {
	if err := decode(&n.cfg); err != nil {
		return err
	}
	if n.cfg.URL == "" {
		n.cfg.URL = "http://127.0.0.1:8222"
	}
	if n.cfg.Timeout <= 0 {
		n.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (n *natsCollector) Init(reg *registry.Registry) error {
	if n.cfg.URL == "" {
		if err := n.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	n.client = &http.Client{Timeout: n.cfg.Timeout}
	if _, err := n.varz(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "nats.server_traffic", Title: "NATS server traffic", Units: "bytes/s", Type: registry.Area, Priority: 51100,
			Dimensions: []*registry.Dimension{{ID: "received", Algorithm: inc}, {ID: "sent", Algorithm: inc, Multiplier: -1}}},
		{ID: "nats.server_messages", Title: "NATS server messages", Units: "messages/s", Priority: 51110,
			Dimensions: []*registry.Dimension{{ID: "received", Algorithm: inc}, {ID: "sent", Algorithm: inc}}},
		{ID: "nats.server_connections", Title: "NATS server connections", Units: "connections", Priority: 51120,
			Dimensions: []*registry.Dimension{{ID: "active"}}},
		{ID: "nats.server_connections_rate", Title: "NATS connections opened", Units: "connections/s", Priority: 51121,
			Dimensions: []*registry.Dimension{{ID: "connections", Algorithm: inc}}},
		{ID: "nats.server_slow_consumers", Title: "NATS slow consumers", Units: "consumers/s", Priority: 51130,
			Dimensions: []*registry.Dimension{{ID: "slow", Algorithm: inc}}},
		{ID: "nats.server_cpu_usage", Title: "NATS CPU usage", Units: "percent", Priority: 51140,
			Dimensions: []*registry.Dimension{{ID: "used"}}},
		{ID: "nats.server_mem_usage", Title: "NATS memory usage", Units: "bytes", Priority: 51150,
			Dimensions: []*registry.Dimension{{ID: "used"}}},
	} {
		c.Family, c.Plugin, c.Module = "nats", "nats", "nats"
		reg.AddChart(c)
	}
	return nil
}

type natsVarz struct {
	Connections   float64 `json:"connections"`
	TotalConns    float64 `json:"total_connections"`
	InMsgs        float64 `json:"in_msgs"`
	OutMsgs       float64 `json:"out_msgs"`
	InBytes       float64 `json:"in_bytes"`
	OutBytes      float64 `json:"out_bytes"`
	SlowConsumers float64 `json:"slow_consumers"`
	Mem           float64 `json:"mem"`
	CPU           float64 `json:"cpu"`
	Subscriptions float64 `json:"subscriptions"`
}

func (n *natsCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	v, err := n.varz(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("nats.server_traffic", now, map[string]float64{"received": v.InBytes, "sent": v.OutBytes})
	_ = reg.Collect("nats.server_messages", now, map[string]float64{"received": v.InMsgs, "sent": v.OutMsgs})
	_ = reg.Collect("nats.server_connections", now, map[string]float64{"active": v.Connections})
	_ = reg.Collect("nats.server_connections_rate", now, map[string]float64{"connections": v.TotalConns})
	_ = reg.Collect("nats.server_slow_consumers", now, map[string]float64{"slow": v.SlowConsumers})
	_ = reg.Collect("nats.server_cpu_usage", now, map[string]float64{"used": v.CPU})
	_ = reg.Collect("nats.server_mem_usage", now, map[string]float64{"used": v.Mem})
	return nil
}

func (n *natsCollector) varz(ctx context.Context) (natsVarz, error) {
	b, err := httpGet(ctx, n.client, stringsTrimSlash(n.cfg.URL)+"/varz")
	if err != nil {
		return natsVarz{}, err
	}
	var v natsVarz
	if err := json.Unmarshal(b, &v); err != nil {
		return natsVarz{}, err
	}
	return v, nil
}

func stringsTrimSlash(s string) string {
	if len(s) > 0 && s[len(s)-1] == '/' {
		return s[:len(s)-1]
	}
	return s
}
