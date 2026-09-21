package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// envoyConfig is collectors.modules.envoy (admin /stats/prometheus).
type envoyConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type envoyCollector struct {
	cfg    envoyConfig
	client *http.Client
	url    string
}

func init() {
	Register("envoy", func() Collector { return &envoyCollector{} })
}

func (e *envoyCollector) Name() string { return "envoy" }

func (e *envoyCollector) Configure(decode func(v any) error) error {
	if err := decode(&e.cfg); err != nil {
		return err
	}
	if e.cfg.Timeout <= 0 {
		e.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (e *envoyCollector) Init(reg *registry.Registry) error {
	if e.cfg.Timeout <= 0 {
		if err := e.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	e.client = &http.Client{Timeout: e.cfg.Timeout}
	urls := []string{e.cfg.URL}
	if e.cfg.URL == "" {
		urls = []string{"http://127.0.0.1:9901/stats/prometheus"}
	}
	u, body, err := httpGetTry(context.Background(), e.client, urls)
	if err != nil {
		return err
	}
	if !strings.Contains(string(body), "envoy_") {
		return fmt.Errorf("envoy: no envoy metrics")
	}
	e.url = u
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "envoy.server_uptime", Title: "Envoy Server Uptime", Units: "seconds", Priority: 55400,
			Dimensions: []*registry.Dimension{{ID: "uptime"}}},
		{ID: "envoy.server_memory_allocated_size", Title: "Envoy Allocated Memory", Units: "bytes", Priority: 55410,
			Dimensions: []*registry.Dimension{{ID: "allocated"}}},
		{ID: "envoy.server_connections_count", Title: "Envoy Server Connections", Units: "connections", Priority: 55420,
			Dimensions: []*registry.Dimension{{ID: "connections"}}},
		{ID: "envoy.cluster_upstream_cx_connect_fail_rate", Title: "Envoy Upstream Connect Failures", Units: "failures/s", Priority: 55430,
			Dimensions: []*registry.Dimension{{ID: "failed", Algorithm: inc}}},
		{ID: "envoy.http_downstream_rq", Title: "Envoy Downstream Requests", Units: "requests/s", Type: registry.Stacked, Priority: 55440,
			Dimensions: []*registry.Dimension{
				{ID: "2xx", Algorithm: inc}, {ID: "4xx", Algorithm: inc}, {ID: "5xx", Algorithm: inc}}},
	} {
		c.Family, c.Plugin, c.Module = "envoy", "envoy", "envoy"
		reg.AddChart(c)
	}
	return nil
}

func (e *envoyCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	b, err := httpGet(ctx, e.client, e.url)
	if err != nil {
		return err
	}
	samples := promSamples(string(b))
	_ = reg.Collect("envoy.server_uptime", now, map[string]float64{
		"uptime": promSum(samples, "envoy_server_uptime")})
	_ = reg.Collect("envoy.server_memory_allocated_size", now, map[string]float64{
		"allocated": promSum(samples, "envoy_server_memory_allocated")})
	_ = reg.Collect("envoy.server_connections_count", now, map[string]float64{
		"connections": promSum(samples, "envoy_server_total_connections")})
	_ = reg.Collect("envoy.cluster_upstream_cx_connect_fail_rate", now, map[string]float64{
		"failed": promSum(samples, "envoy_cluster_upstream_cx_connect_fail")})
	xx := promSumByLabel(samples, "envoy_http_downstream_rq_xx", "envoy_response_code_class")
	if len(xx) == 0 {
		xx = promSumByLabel(samples, "envoy_http_downstream_rq_xx", "code")
	}
	_ = reg.Collect("envoy.http_downstream_rq", now, map[string]float64{
		"2xx": xx["2xx"] + xx["2"], "4xx": xx["4xx"] + xx["4"], "5xx": xx["5xx"] + xx["5"]})
	return nil
}
