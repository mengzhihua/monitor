package collect

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// clickhouseConfig is collectors.modules.clickhouse (HTTP :8123).
type clickhouseConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type clickhouseCollector struct {
	cfg    clickhouseConfig
	client *http.Client
	url    string
}

func init() {
	Register("clickhouse", func() Collector { return &clickhouseCollector{} })
}

func (c *clickhouseCollector) Name() string { return "clickhouse" }

func (c *clickhouseCollector) Configure(decode func(v any) error) error {
	if err := decode(&c.cfg); err != nil {
		return err
	}
	if c.cfg.Timeout <= 0 {
		c.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (c *clickhouseCollector) Init(reg *registry.Registry) error {
	if c.cfg.Timeout <= 0 {
		if err := c.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	c.client = &http.Client{Timeout: c.cfg.Timeout}
	base := strings.TrimRight(c.cfg.URL, "/")
	if base == "" {
		base = "http://127.0.0.1:8123"
	}
	c.url = base
	if _, _, err := c.metrics(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "clickhouse.connections", Title: "ClickHouse Connections", Units: "connections", Priority: 55100,
			Dimensions: []*registry.Dimension{{ID: "tcp"}, {ID: "http"}, {ID: "mysql"}, {ID: "interserver"}}},
		{ID: "clickhouse.memory_usage", Title: "ClickHouse Memory Usage", Units: "bytes", Priority: 55110,
			Dimensions: []*registry.Dimension{{ID: "memory"}}},
		{ID: "clickhouse.queries", Title: "ClickHouse Queries", Units: "queries/s", Priority: 55120,
			Dimensions: []*registry.Dimension{{ID: "queries", Algorithm: inc}}},
		{ID: "clickhouse.select_queries", Title: "ClickHouse SELECT Queries", Units: "queries/s", Priority: 55130,
			Dimensions: []*registry.Dimension{{ID: "select", Algorithm: inc}}},
		{ID: "clickhouse.insert_queries", Title: "ClickHouse INSERT Queries", Units: "queries/s", Priority: 55140,
			Dimensions: []*registry.Dimension{{ID: "insert", Algorithm: inc}}},
		{ID: "clickhouse.running_queries", Title: "ClickHouse Running Queries", Units: "queries", Priority: 55150,
			Dimensions: []*registry.Dimension{{ID: "query"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "clickhouse", "clickhouse", "clickhouse"
		reg.AddChart(ch)
	}
	return nil
}

func (c *clickhouseCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, ev, err := c.metrics(ctx)
	if err != nil {
		return err
	}
	g := func(k string) float64 { return m[k] }
	e := func(k string) float64 { return ev[k] }
	_ = reg.Collect("clickhouse.connections", now, map[string]float64{
		"tcp": g("TCPConnection"), "http": g("HTTPConnection"), "mysql": g("MySQLConnection"), "interserver": g("InterserverConnection")})
	_ = reg.Collect("clickhouse.memory_usage", now, map[string]float64{"memory": g("MemoryTracking")})
	_ = reg.Collect("clickhouse.queries", now, map[string]float64{"queries": e("Query")})
	_ = reg.Collect("clickhouse.select_queries", now, map[string]float64{"select": e("SelectQuery")})
	_ = reg.Collect("clickhouse.insert_queries", now, map[string]float64{"insert": e("InsertQuery")})
	_ = reg.Collect("clickhouse.running_queries", now, map[string]float64{"query": g("Query")})
	return nil
}

func (c *clickhouseCollector) metrics(ctx context.Context) (metrics, events map[string]float64, err error) {
	metrics, err = c.tabQuery(ctx, "SELECT metric, value FROM system.metrics")
	if err != nil {
		return nil, nil, err
	}
	events, err = c.tabQuery(ctx, "SELECT event, value FROM system.events")
	if err != nil {
		return nil, nil, err
	}
	if len(metrics) == 0 && len(events) == 0 {
		return nil, nil, fmt.Errorf("clickhouse: no metrics")
	}
	return metrics, events, nil
}

func (c *clickhouseCollector) tabQuery(ctx context.Context, q string) (map[string]float64, error) {
	u := c.url + "/?query=" + url.QueryEscape(q)
	b, err := httpGet(ctx, c.client, u)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: %w", err)
	}
	out := map[string]float64{}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		out[fields[0]] = firstFloat(fields[1])
	}
	return out, nil
}
