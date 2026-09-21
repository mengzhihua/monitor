package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// varnishConfig is collectors.modules.varnish (varnishstat -j).
type varnishConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type varnishCollector struct {
	cfg varnishConfig
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func init() {
	Register("varnish", func() Collector { return &varnishCollector{} })
}

func (v *varnishCollector) Name() string { return "varnish" }

func (v *varnishCollector) Configure(decode func(v any) error) error {
	if err := decode(&v.cfg); err != nil {
		return err
	}
	if v.cfg.Command == "" {
		v.cfg.Command = "varnishstat"
	}
	if v.cfg.Timeout <= 0 {
		v.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (v *varnishCollector) Init(reg *registry.Registry) error {
	if v.cfg.Command == "" {
		if err := v.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := v.stats(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "varnish.client_requests", Title: "Client Requests", Units: "requests/s", Priority: 51200,
			Dimensions: []*registry.Dimension{{ID: "received", Algorithm: inc}}},
		{ID: "varnish.cache_hit_ratio_total", Title: "Cache Hit Ratio Total", Units: "percent", Type: registry.Stacked, Priority: 51210,
			Dimensions: []*registry.Dimension{
				{ID: "hit", Algorithm: registry.PercentageOfAbsoluteRow},
				{ID: "miss", Algorithm: registry.PercentageOfAbsoluteRow},
				{ID: "hitpass", Algorithm: registry.PercentageOfAbsoluteRow}}},
		{ID: "varnish.backends_requests", Title: "Backend Requests", Units: "requests/s", Priority: 51220,
			Dimensions: []*registry.Dimension{{ID: "sent", Algorithm: inc}}},
		{ID: "varnish.cache_expired_objects", Title: "Cache Expired Objects", Units: "objects/s", Priority: 51230,
			Dimensions: []*registry.Dimension{{ID: "expired", Algorithm: inc}}},
		{ID: "varnish.cache_lru_activity", Title: "Cache LRU Activity", Units: "objects/s", Priority: 51231,
			Dimensions: []*registry.Dimension{{ID: "nuked", Algorithm: inc}, {ID: "moved", Algorithm: inc}}},
		{ID: "varnish.threads", Title: "Threads In All Pools", Units: "threads", Priority: 51240,
			Dimensions: []*registry.Dimension{{ID: "threads"}}},
		{ID: "varnish.backends_connections", Title: "Backend Connections", Units: "connections/s", Priority: 51250,
			Dimensions: []*registry.Dimension{
				{ID: "successful", Algorithm: inc}, {ID: "unhealthy", Algorithm: inc},
				{ID: "failed", Algorithm: inc}, {ID: "reused", Algorithm: inc}}},
	} {
		c.Family, c.Plugin, c.Module = "varnish", "varnish", "varnish"
		reg.AddChart(c)
	}
	return nil
}

func (v *varnishCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	s, err := v.stats(ctx)
	if err != nil {
		return err
	}
	g := func(k string) float64 { return s[k] }
	_ = reg.Collect("varnish.client_requests", now, map[string]float64{"received": g("MAIN.client_req")})
	_ = reg.Collect("varnish.cache_hit_ratio_total", now, map[string]float64{
		"hit": g("MAIN.cache_hit"), "miss": g("MAIN.cache_miss"), "hitpass": g("MAIN.cache_hitpass")})
	_ = reg.Collect("varnish.backends_requests", now, map[string]float64{"sent": g("MAIN.backend_req")})
	_ = reg.Collect("varnish.cache_expired_objects", now, map[string]float64{"expired": g("MAIN.n_expired")})
	_ = reg.Collect("varnish.cache_lru_activity", now, map[string]float64{
		"nuked": g("MAIN.n_lru_nuked"), "moved": g("MAIN.n_lru_moved")})
	_ = reg.Collect("varnish.threads", now, map[string]float64{"threads": g("MAIN.threads")})
	_ = reg.Collect("varnish.backends_connections", now, map[string]float64{
		"successful": g("MAIN.backend_conn"), "unhealthy": g("MAIN.backend_unhealthy"),
		"failed": g("MAIN.backend_fail"), "reused": g("MAIN.backend_reuse")})
	return nil
}

func (v *varnishCollector) stats(ctx context.Context) (map[string]float64, error) {
	run := v.run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cctx, cancel := context.WithTimeout(ctx, v.cfg.Timeout)
			defer cancel()
			return exec.CommandContext(cctx, name, args...).Output()
		}
	}
	out, err := run(ctx, v.cfg.Command, "-j")
	if err != nil {
		return nil, fmt.Errorf("varnishstat: %w", err)
	}
	m, err := parseVarnishJSON(out)
	if err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, fmt.Errorf("varnishstat: empty")
	}
	return m, nil
}

func parseVarnishJSON(b []byte) (map[string]float64, error) {
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	out := map[string]float64{}
	counters, _ := raw["counters"].(map[string]any)
	if counters == nil {
		counters = raw
	}
	for k, v := range counters {
		if !strings.Contains(k, ".") {
			continue
		}
		switch t := v.(type) {
		case map[string]any:
			out[k] = jsonNum(t["value"])
		case float64:
			out[k] = t
		}
	}
	return out, nil
}
