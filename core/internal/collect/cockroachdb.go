package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// cockroachdbConfig is collectors.modules.cockroachdb (/_status/vars).
type cockroachdbConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type cockroachdbCollector struct {
	cfg    cockroachdbConfig
	client *http.Client
	url    string
}

func init() {
	Register("cockroachdb", func() Collector { return &cockroachdbCollector{} })
}

func (c *cockroachdbCollector) Name() string { return "cockroachdb" }

func (c *cockroachdbCollector) Configure(decode func(v any) error) error {
	if err := decode(&c.cfg); err != nil {
		return err
	}
	if c.cfg.Timeout <= 0 {
		c.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (c *cockroachdbCollector) Init(reg *registry.Registry) error {
	if c.cfg.Timeout <= 0 {
		if err := c.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	c.client = &http.Client{Timeout: c.cfg.Timeout}
	urls := []string{c.cfg.URL}
	if c.cfg.URL == "" {
		urls = []string{"http://127.0.0.1:8080/_status/vars"}
	}
	u, body, err := httpGetTry(context.Background(), c.client, urls)
	if err != nil {
		return err
	}
	if !strings.Contains(string(body), "sql_") && !strings.Contains(string(body), "sys_rss") {
		return fmt.Errorf("cockroachdb: no cockroach metrics")
	}
	c.url = u
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "cockroachdb.sql_connections", Title: "SQL Connections", Units: "connections", Priority: 55200,
			Dimensions: []*registry.Dimension{{ID: "connections"}}},
		{ID: "cockroachdb.sql_statements_total", Title: "SQL Statements", Units: "statements/s", Priority: 55210,
			Dimensions: []*registry.Dimension{{ID: "statements", Algorithm: inc}}},
		{ID: "cockroachdb.live_nodes", Title: "Live Nodes", Units: "nodes", Priority: 55220,
			Dimensions: []*registry.Dimension{{ID: "live_nodes"}}},
		{ID: "cockroachdb.process_memory", Title: "Process Memory", Units: "bytes", Priority: 55230,
			Dimensions: []*registry.Dimension{{ID: "rss"}}},
		{ID: "cockroachdb.process_uptime", Title: "Process Uptime", Units: "seconds", Priority: 55240,
			Dimensions: []*registry.Dimension{{ID: "uptime"}}},
		{ID: "cockroachdb.storage_usable_capacity", Title: "Storage Usable Capacity", Units: "bytes", Type: registry.Stacked, Priority: 55250,
			Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "available"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "cockroachdb", "cockroachdb", "cockroachdb"
		reg.AddChart(ch)
	}
	return nil
}

func (c *cockroachdbCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	b, err := httpGet(ctx, c.client, c.url)
	if err != nil {
		return err
	}
	samples := promSamples(string(b))
	sqlConns := promSumPrefixed(samples, "sql_conns", "sql_connections")
	stmts := promSumPrefixed(samples, "sql_querycount", "sql_query_count", "sql_statement_combined_count")
	_ = reg.Collect("cockroachdb.sql_connections", now, map[string]float64{"connections": sqlConns})
	_ = reg.Collect("cockroachdb.sql_statements_total", now, map[string]float64{"statements": stmts})
	_ = reg.Collect("cockroachdb.live_nodes", now, map[string]float64{
		"live_nodes": promSumPrefixed(samples, "liveness_livenodes", "replicas_leaders")})
	_ = reg.Collect("cockroachdb.process_memory", now, map[string]float64{"rss": promSum(samples, "sys_rss")})
	_ = reg.Collect("cockroachdb.process_uptime", now, map[string]float64{"uptime": promSum(samples, "sys_uptime")})
	_ = reg.Collect("cockroachdb.storage_usable_capacity", now, map[string]float64{
		"used":      promSumPrefixed(samples, "capacity_used", "sys_host_disk_used_bytes"),
		"available": promSumPrefixed(samples, "capacity_available", "capacity"),
	})
	return nil
}
