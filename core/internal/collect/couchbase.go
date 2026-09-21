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

// couchbaseConfig is collectors.modules.couchbase (HTTP /pools/default/buckets).
type couchbaseConfig struct {
	URL      string        `yaml:"url"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type couchbaseCollector struct {
	cfg    couchbaseConfig
	client *http.Client
	url    string
}

func init() {
	Register("couchbase", func() Collector { return &couchbaseCollector{} })
}

func (c *couchbaseCollector) Name() string { return "couchbase" }

func (c *couchbaseCollector) Configure(decode func(v any) error) error {
	if err := decode(&c.cfg); err != nil {
		return err
	}
	if c.cfg.Timeout <= 0 {
		c.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (c *couchbaseCollector) Init(reg *registry.Registry) error {
	if c.cfg.Timeout <= 0 {
		if err := c.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	c.client = &http.Client{Timeout: c.cfg.Timeout}
	base := strings.TrimRight(c.cfg.URL, "/")
	if base == "" {
		base = "http://127.0.0.1:8091"
	}
	c.url = base
	buckets, err := c.buckets(context.Background())
	if err != nil {
		return err
	}
	if len(buckets) == 0 {
		return fmt.Errorf("couchbase: no buckets")
	}
	for _, ch := range []*registry.Chart{
		{ID: "couchbase.bucket_quota_percent_used", Title: "Quota Percent Used Per Bucket", Units: "%", Priority: 56500},
		{ID: "couchbase.bucket_ops_per_sec", Title: "Operations Per Second Per Bucket", Units: "ops/s", Type: registry.Stacked, Priority: 56510},
		{ID: "couchbase.bucket_item_count", Title: "Item Count Per Bucket", Units: "items", Type: registry.Stacked, Priority: 56520},
		{ID: "couchbase.bucket_mem_used", Title: "Memory Used Per Bucket", Units: "bytes", Type: registry.Stacked, Priority: 56530},
		{ID: "couchbase.bucket_disk_used_stats", Title: "Disk Used Per Bucket", Units: "bytes", Type: registry.Stacked, Priority: 56540},
	} {
		ch.Family, ch.Plugin, ch.Module = "couchbase", "couchbase", "couchbase"
		reg.AddChart(ch)
	}
	return nil
}

func (c *couchbaseCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	buckets, err := c.buckets(ctx)
	if err != nil {
		return err
	}
	quota, ops, items, mem, disk := map[string]float64{}, map[string]float64{}, map[string]float64{}, map[string]float64{}, map[string]float64{}
	for _, b := range buckets {
		name, _ := b["name"].(string)
		id := sanitizeID(name)
		if id == "" {
			continue
		}
		basic, _ := b["basicStats"].(map[string]any)
		ensureDim(reg, "couchbase.bucket_quota_percent_used", id, &registry.Dimension{ID: id, Name: name})
		ensureDim(reg, "couchbase.bucket_ops_per_sec", id, &registry.Dimension{ID: id, Name: name})
		ensureDim(reg, "couchbase.bucket_item_count", id, &registry.Dimension{ID: id, Name: name})
		ensureDim(reg, "couchbase.bucket_mem_used", id, &registry.Dimension{ID: id, Name: name})
		ensureDim(reg, "couchbase.bucket_disk_used_stats", id, &registry.Dimension{ID: id, Name: name})
		quota[id] = nestFloat(basic, "quotaPercentUsed")
		ops[id] = nestFloat(basic, "opsPerSec")
		items[id] = nestFloat(basic, "itemCount")
		mem[id] = nestFloat(basic, "memUsed")
		disk[id] = nestFloat(basic, "diskUsed")
	}
	_ = reg.Collect("couchbase.bucket_quota_percent_used", now, quota)
	_ = reg.Collect("couchbase.bucket_ops_per_sec", now, ops)
	_ = reg.Collect("couchbase.bucket_item_count", now, items)
	_ = reg.Collect("couchbase.bucket_mem_used", now, mem)
	_ = reg.Collect("couchbase.bucket_disk_used_stats", now, disk)
	return nil
}

func (c *couchbaseCollector) buckets(ctx context.Context) ([]map[string]any, error) {
	b, err := httpGetAuth(ctx, c.client, c.url+"/pools/default/buckets", c.cfg.User, c.cfg.Password)
	if err != nil {
		return nil, fmt.Errorf("couchbase: %w", err)
	}
	var arr []map[string]any
	if err := json.Unmarshal(b, &arr); err != nil {
		return nil, fmt.Errorf("couchbase: %w", err)
	}
	return arr, nil
}
