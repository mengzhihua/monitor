package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// gethConfig is collectors.modules.geth (debug metrics Prometheus).
type gethConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type gethCollector struct {
	cfg    gethConfig
	client *http.Client
	url    string
}

func init() {
	Register("geth", func() Collector { return &gethCollector{} })
}

func (g *gethCollector) Name() string { return "geth" }

func (g *gethCollector) Configure(decode func(v any) error) error {
	if err := decode(&g.cfg); err != nil {
		return err
	}
	if g.cfg.Timeout <= 0 {
		g.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (g *gethCollector) Init(reg *registry.Registry) error {
	if g.cfg.Timeout <= 0 {
		if err := g.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	g.client = &http.Client{Timeout: g.cfg.Timeout}
	urls := []string{g.cfg.URL}
	if g.cfg.URL == "" {
		urls = []string{
			"http://127.0.0.1:6060/debug/metrics/prometheus",
			"http://127.0.0.1:6060/debug/metrics",
		}
	}
	u, body, err := httpGetTry(context.Background(), g.client, urls)
	if err != nil {
		return err
	}
	g.url = u
	if !gethIsMetrics(string(body)) {
		return fmt.Errorf("geth: not geth metrics")
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "geth.chainhead", Title: "Chainhead", Units: "block", Priority: 57800,
			Dimensions: []*registry.Dimension{{ID: "block"}, {ID: "receipt"}, {ID: "header"}}},
		{ID: "geth.p2p_peers", Title: "Number of Peers", Units: "peers", Priority: 57810,
			Dimensions: []*registry.Dimension{{ID: "peers"}}},
		{ID: "geth.rpc_calls", Title: "rpc calls", Units: "calls/s", Priority: 57820,
			Dimensions: []*registry.Dimension{{ID: "failed", Algorithm: inc}, {ID: "successful", Algorithm: inc}}},
		{ID: "geth.goroutines", Title: "Number of goroutines", Units: "goroutines", Priority: 57830,
			Dimensions: []*registry.Dimension{{ID: "goroutines"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "geth", "geth", "geth"
		reg.AddChart(ch)
	}
	return nil
}

func (g *gethCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	b, err := httpGet(ctx, g.client, g.url)
	if err != nil {
		return fmt.Errorf("geth: %w", err)
	}
	samples := promSamples(string(b))
	_ = reg.Collect("geth.chainhead", now, map[string]float64{
		"block":   promSum(samples, "chain_head_block"),
		"receipt": promSum(samples, "chain_head_receipt"),
		"header":  promSum(samples, "chain_head_header"),
	})
	_ = reg.Collect("geth.p2p_peers", now, map[string]float64{"peers": promSum(samples, "p2p_peers")})
	_ = reg.Collect("geth.rpc_calls", now, map[string]float64{
		"failed":     promSum(samples, "rpc_failure"),
		"successful": promSum(samples, "rpc_success"),
	})
	gr := promSum(samples, "system_cpu_goroutines")
	if gr == 0 {
		gr = promSum(samples, "go_goroutines")
	}
	_ = reg.Collect("geth.goroutines", now, map[string]float64{"goroutines": gr})
	return nil
}

func gethIsMetrics(body string) bool {
	return strings.Contains(body, "chain_head_block") || strings.Contains(body, "p2p_peers") || strings.Contains(body, "rpc_success")
}
