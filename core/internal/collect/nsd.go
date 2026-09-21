package collect

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// nsdConfig is collectors.modules.nsd (`nsd-control stats`).
type nsdConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type nsdCollector struct {
	cfg nsdConfig
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func init() {
	Register("nsd", func() Collector { return &nsdCollector{} })
}

func (n *nsdCollector) Name() string { return "nsd" }

func (n *nsdCollector) Configure(decode func(v any) error) error {
	if err := decode(&n.cfg); err != nil {
		return err
	}
	if n.cfg.Command == "" {
		n.cfg.Command = "nsd-control"
	}
	if n.cfg.Timeout <= 0 {
		n.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (n *nsdCollector) Init(reg *registry.Registry) error {
	if n.cfg.Command == "" {
		if err := n.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if n.run == nil {
		n.run = execRun(n.cfg.Timeout)
	}
	if _, err := n.stats(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "nsd.queries", Title: "Queries", Units: "queries/s", Priority: 58700,
			Dimensions: []*registry.Dimension{{ID: "queries", Algorithm: inc}}},
		{ID: "nsd.queries_by_protocol", Title: "Queries Protocol", Units: "queries/s", Type: registry.Stacked, Priority: 58710,
			Dimensions: []*registry.Dimension{
				{ID: "udp", Algorithm: inc}, {ID: "udp6", Algorithm: inc}, {ID: "tcp", Algorithm: inc},
				{ID: "tcp6", Algorithm: inc}, {ID: "tls", Algorithm: inc}, {ID: "tls6", Algorithm: inc},
			}},
		{ID: "nsd.errors", Title: "Errors", Units: "errors/s", Priority: 58720,
			Dimensions: []*registry.Dimension{{ID: "query", Algorithm: inc}, {ID: "answer", Algorithm: inc, Multiplier: -1}}},
		{ID: "nsd.drops", Title: "Drops", Units: "drops/s", Priority: 58730,
			Dimensions: []*registry.Dimension{{ID: "query", Algorithm: inc}}},
		{ID: "nsd.zones", Title: "Zones", Units: "zones", Priority: 58740,
			Dimensions: []*registry.Dimension{{ID: "master"}, {ID: "slave"}}},
		{ID: "nsd.uptime", Title: "Uptime", Units: "seconds", Priority: 58750,
			Dimensions: []*registry.Dimension{{ID: "uptime"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "nsd", "nsd", "nsd"
		reg.AddChart(ch)
	}
	return nil
}

func (n *nsdCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	st, err := n.stats(ctx)
	if err != nil {
		return err
	}
	g := func(k string) float64 { return st[k] }
	_ = reg.Collect("nsd.queries", now, map[string]float64{"queries": g("num.queries")})
	_ = reg.Collect("nsd.queries_by_protocol", now, map[string]float64{
		"udp": g("num.udp"), "udp6": g("num.udp6"), "tcp": g("num.tcp"), "tcp6": g("num.tcp6"), "tls": g("num.tls"), "tls6": g("num.tls6"),
	})
	_ = reg.Collect("nsd.errors", now, map[string]float64{"query": g("num.rxerr"), "answer": g("num.txerr")})
	_ = reg.Collect("nsd.drops", now, map[string]float64{"query": g("num.dropped")})
	_ = reg.Collect("nsd.zones", now, map[string]float64{"master": g("zone.master"), "slave": g("zone.slave")})
	_ = reg.Collect("nsd.uptime", now, map[string]float64{"uptime": g("time.boot")})
	return nil
}

func (n *nsdCollector) stats(ctx context.Context) (map[string]float64, error) {
	b, err := n.run(ctx, n.cfg.Command, "stats")
	if err != nil {
		return nil, fmt.Errorf("nsd: %w", err)
	}
	out := map[string]float64{}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		out[k] = firstFloat(v)
	}
	if _, ok := out["num.queries"]; !ok {
		return nil, fmt.Errorf("nsd: no stats")
	}
	return out, nil
}
