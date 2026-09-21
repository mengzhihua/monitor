package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// couchdbConfig is collectors.modules.couchdb (HTTP /_stats).
type couchdbConfig struct {
	URL      string        `yaml:"url"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type couchdbCollector struct {
	cfg    couchdbConfig
	client *http.Client
	url    string
}

func init() {
	Register("couchdb", func() Collector { return &couchdbCollector{} })
}

func (c *couchdbCollector) Name() string { return "couchdb" }

func (c *couchdbCollector) Configure(decode func(v any) error) error {
	if err := decode(&c.cfg); err != nil {
		return err
	}
	if c.cfg.Timeout <= 0 {
		c.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (c *couchdbCollector) Init(reg *registry.Registry) error {
	if c.cfg.Timeout <= 0 {
		if err := c.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	c.client = &http.Client{Timeout: c.cfg.Timeout}
	base := strings.TrimRight(c.cfg.URL, "/")
	if base == "" {
		base = "http://127.0.0.1:5984"
	}
	c.url = base
	if _, _, err := c.stats(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "couchdb.activity", Title: "Overall Activity", Units: "requests/s", Type: registry.Stacked, Priority: 56400,
			Dimensions: []*registry.Dimension{{ID: "DB reads", Algorithm: inc}, {ID: "DB writes", Algorithm: inc}, {ID: "View reads", Algorithm: inc}}},
		{ID: "couchdb.response_code_classes", Title: "HTTP response status code classes", Units: "responses/s", Type: registry.Stacked, Priority: 56410,
			Dimensions: []*registry.Dimension{{ID: "2xx", Name: "2xx Success", Algorithm: inc}, {ID: "3xx", Name: "3xx Redirection", Algorithm: inc}, {ID: "4xx", Name: "4xx Client error", Algorithm: inc}, {ID: "5xx", Name: "5xx Server error", Algorithm: inc}}},
		{ID: "couchdb.active_tasks", Title: "Active task breakdown", Units: "tasks", Type: registry.Stacked, Priority: 56420,
			Dimensions: []*registry.Dimension{{ID: "Indexer"}, {ID: "DB Compaction"}, {ID: "Replication"}, {ID: "View Compaction"}}},
		{ID: "couchdb.open_files", Title: "Open files", Units: "files", Priority: 56430,
			Dimensions: []*registry.Dimension{{ID: "# files"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "couchdb", "couchdb", "couchdb"
		reg.AddChart(ch)
	}
	return nil
}

func (c *couchdbCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, tasks, err := c.stats(ctx)
	if err != nil {
		return err
	}
	stats := nestMap(m, "couchdb")
	if stats == nil {
		stats = m
	}
	_ = reg.Collect("couchdb.activity", now, map[string]float64{
		"DB reads":   couchdbMetric(stats, "database_reads"),
		"DB writes":  couchdbMetric(stats, "database_writes"),
		"View reads": couchdbMetric(nestMap(stats, "httpd"), "view_reads") + couchdbMetric(stats, "httpd_view_reads"),
	})
	codes := nestMap(stats, "httpd_status_codes")
	if codes == nil {
		codes = nestMap(m, "couchdb", "httpd_status_codes")
	}
	var c2, c3, c4, c5 float64
	for k, v := range codes {
		n := nestFloat(map[string]any{"v": v}, "v", "value")
		if n == 0 {
			n = nestFloat(map[string]any{"v": v}, "v")
		}
		if len(k) > 0 {
			switch k[0] {
			case '2':
				c2 += n
			case '3':
				c3 += n
			case '4':
				c4 += n
			case '5':
				c5 += n
			}
		}
	}
	_ = reg.Collect("couchdb.response_code_classes", now, map[string]float64{
		"2xx": c2, "3xx": c3, "4xx": c4, "5xx": c5,
	})
	var idx, compact, repl, vcompact float64
	for _, t := range tasks {
		tm, _ := t.(map[string]any)
		switch nestString(tm, "type") {
		case "indexer":
			idx++
		case "database_compaction":
			compact++
		case "replication":
			repl++
		case "view_compaction":
			vcompact++
		}
	}
	_ = reg.Collect("couchdb.active_tasks", now, map[string]float64{
		"Indexer": idx, "DB Compaction": compact, "Replication": repl, "View Compaction": vcompact,
	})
	_ = reg.Collect("couchdb.open_files", now, map[string]float64{"# files": couchdbMetric(stats, "open_os_files")})
	return nil
}

func couchdbMetric(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	v := nestFloat(m, key, "value")
	if v == 0 {
		v = nestFloat(m, key)
	}
	return v
}

func (c *couchdbCollector) stats(ctx context.Context) (map[string]any, []any, error) {
	urls := []string{c.url + "/_node/_local/_stats", c.url + "/_stats"}
	var last error
	var m map[string]any
	for _, u := range urls {
		b, err := httpGetAuth(ctx, c.client, u, c.cfg.User, c.cfg.Password)
		if err != nil {
			last = err
			continue
		}
		m, err = jsonMap(b)
		if err != nil {
			last = err
			continue
		}
		last = nil
		break
	}
	if m == nil {
		if last == nil {
			last = fmt.Errorf("no stats")
		}
		return nil, nil, fmt.Errorf("couchdb: %w", last)
	}
	tb, err := httpGetAuth(ctx, c.client, c.url+"/_active_tasks", c.cfg.User, c.cfg.Password)
	var tasks []any
	if err == nil {
		_ = json.Unmarshal(tb, &tasks)
	}
	return m, tasks, nil
}
