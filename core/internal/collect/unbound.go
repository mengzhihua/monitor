package collect

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// unboundConfig is collectors.modules.unbound (unbound-control stats).
type unboundConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type unboundCollector struct {
	cfg unboundConfig
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func init() {
	Register("unbound", func() Collector { return &unboundCollector{} })
}

func (u *unboundCollector) Name() string { return "unbound" }

func (u *unboundCollector) Configure(decode func(v any) error) error {
	if err := decode(&u.cfg); err != nil {
		return err
	}
	if u.cfg.Command == "" {
		u.cfg.Command = "unbound-control"
	}
	if u.cfg.Timeout <= 0 {
		u.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (u *unboundCollector) Init(reg *registry.Registry) error {
	if u.cfg.Command == "" {
		if err := u.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := u.stats(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "unbound.queries", Title: "Received Queries", Units: "queries/s", Priority: 51700,
			Dimensions: []*registry.Dimension{{ID: "queries", Algorithm: inc}}},
		{ID: "unbound.cache", Title: "Cache Statistics", Units: "events/s", Type: registry.Stacked, Priority: 51710,
			Dimensions: []*registry.Dimension{{ID: "hits", Algorithm: inc}, {ID: "miss", Algorithm: inc}}},
		{ID: "unbound.prefetch", Title: "Cache Prefetches", Units: "prefetches/s", Priority: 51711,
			Dimensions: []*registry.Dimension{{ID: "prefetches", Algorithm: inc}}},
		{ID: "unbound.recursive_replies", Title: "Replies That Needed Recursive Processing", Units: "replies/s", Priority: 51720,
			Dimensions: []*registry.Dimension{{ID: "recursive", Algorithm: inc}}},
		{ID: "unbound.request_list_usage", Title: "Request List Usage", Units: "queries", Priority: 51730,
			Dimensions: []*registry.Dimension{{ID: "avg"}, {ID: "max"}}},
		{ID: "unbound.request_list_jostle_list", Title: "Request List Jostle List Events", Units: "queries/s", Priority: 51731,
			Dimensions: []*registry.Dimension{{ID: "overwritten", Algorithm: inc}, {ID: "dropped", Algorithm: inc}}},
	} {
		c.Family, c.Plugin, c.Module = "unbound", "unbound", "unbound"
		reg.AddChart(c)
	}
	return nil
}

func (u *unboundCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := u.stats(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("unbound.queries", now, map[string]float64{"queries": m["total.num.queries"]})
	_ = reg.Collect("unbound.cache", now, map[string]float64{
		"hits": m["total.num.cachehits"], "miss": m["total.num.cachemiss"]})
	_ = reg.Collect("unbound.prefetch", now, map[string]float64{"prefetches": m["total.num.prefetch"]})
	_ = reg.Collect("unbound.recursive_replies", now, map[string]float64{"recursive": m["total.num.recursivereplies"]})
	_ = reg.Collect("unbound.request_list_usage", now, map[string]float64{
		"avg": m["total.requestlist.avg"], "max": m["total.requestlist.max"]})
	_ = reg.Collect("unbound.request_list_jostle_list", now, map[string]float64{
		"overwritten": m["total.requestlist.overwritten"], "dropped": m["total.requestlist.exceeded"]})
	return nil
}

func (u *unboundCollector) stats(ctx context.Context) (map[string]float64, error) {
	run := u.run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cctx, cancel := context.WithTimeout(ctx, u.cfg.Timeout)
			defer cancel()
			return exec.CommandContext(cctx, name, args...).Output()
		}
	}
	out, err := run(ctx, u.cfg.Command, "stats")
	if err != nil {
		return nil, fmt.Errorf("unbound-control: %w", err)
	}
	m := parseUnboundStats(string(out))
	if _, ok := m["total.num.queries"]; !ok {
		return nil, fmt.Errorf("unbound: no total.num.queries")
	}
	return m, nil
}

func parseUnboundStats(s string) map[string]float64 {
	out := map[string]float64{}
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = firstFloat(v)
	}
	return out
}
