package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// yugabytedbConfig is collectors.modules.yugabytedb (prometheus_metrics).
type yugabytedbConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type yugabytedbCollector struct {
	cfg    yugabytedbConfig
	client *http.Client
	url    string
}

func init() {
	Register("yugabytedb", func() Collector { return &yugabytedbCollector{} })
}

func (y *yugabytedbCollector) Name() string { return "yugabytedb" }

func (y *yugabytedbCollector) Configure(decode func(v any) error) error {
	if err := decode(&y.cfg); err != nil {
		return err
	}
	if y.cfg.Timeout <= 0 {
		y.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (y *yugabytedbCollector) Init(reg *registry.Registry) error {
	if y.cfg.Timeout <= 0 {
		if err := y.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	y.client = &http.Client{Timeout: y.cfg.Timeout}
	urls := []string{y.cfg.URL}
	if y.cfg.URL == "" {
		urls = []string{
			"http://127.0.0.1:13000/prometheus-metrics",
			"http://127.0.0.1:13000/prometheus_metrics",
			"http://127.0.0.1:9000/prometheus-metrics",
			"http://127.0.0.1:9000/prometheus_metrics",
			"http://127.0.0.1:7000/prometheus-metrics",
			"http://127.0.0.1:7000/prometheus_metrics",
			"http://127.0.0.1:12000/prometheus-metrics",
			"http://127.0.0.1:12000/prometheus_metrics",
		}
	}
	u, body, err := httpGetTry(context.Background(), y.client, urls)
	if err != nil {
		return err
	}
	y.url = u
	if !yugabyteIsMetrics(string(body)) {
		return fmt.Errorf("yugabytedb: not yugabyte metrics")
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "yugabytedb.ysql_connection_usage", Title: "YSQL Connections Usage", Units: "connections", Type: registry.Stacked, Priority: 59400,
			Dimensions: []*registry.Dimension{{ID: "available"}, {ID: "used"}}},
		{ID: "yugabytedb.ysql_active_connections", Title: "YSQL Active Connections", Units: "connections", Priority: 59410,
			Dimensions: []*registry.Dimension{{ID: "active"}}},
		{ID: "yugabytedb.ysql_over_limit_connections", Title: "YSQL Rejected Over Limit Connections", Units: "rejects/s", Priority: 59420,
			Dimensions: []*registry.Dimension{{ID: "over_limit", Algorithm: inc}}},
		{ID: "yugabytedb.ysql_sql_statements", Title: "YSQL SQL Statements", Units: "statements/s", Priority: 59430,
			Dimensions: []*registry.Dimension{{ID: "statements", Algorithm: inc}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "yugabytedb", "yugabytedb", "yugabytedb"
		reg.AddChart(ch)
	}
	return nil
}

func (y *yugabytedbCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	b, err := httpGet(ctx, y.client, y.url)
	if err != nil {
		return fmt.Errorf("yugabytedb: %w", err)
	}
	samples := promSamples(string(b))
	used := promSum(samples, "yb_ysqlserver_connection_total")
	maxc := promSum(samples, "yb_ysqlserver_max_connection_total")
	avail := maxc - used
	if avail < 0 {
		avail = 0
	}
	_ = reg.Collect("yugabytedb.ysql_connection_usage", now, map[string]float64{"available": avail, "used": used})
	_ = reg.Collect("yugabytedb.ysql_active_connections", now, map[string]float64{"active": promSum(samples, "yb_ysqlserver_active_connection_total")})
	_ = reg.Collect("yugabytedb.ysql_over_limit_connections", now, map[string]float64{"over_limit": promSum(samples, "yb_ysqlserver_connection_over_limit_total")})
	stmts := 0.0
	for _, s := range samples {
		if strings.Contains(s.Name, "handler_latency_yb_ysqlserver_SQLProcessor") && strings.HasSuffix(s.Name, "_count") {
			stmts += s.Value
		}
	}
	_ = reg.Collect("yugabytedb.ysql_sql_statements", now, map[string]float64{"statements": stmts})
	return nil
}

func yugabyteIsMetrics(body string) bool {
	return strings.Contains(body, "yb_ysqlserver") || strings.Contains(body, "handler_latency_yb_") || strings.Contains(body, "hybrid_clock_skew")
}
