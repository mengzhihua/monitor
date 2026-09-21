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

// fluentdConfig is collectors.modules.fluentd (monitor_agent /api/plugins.json).
type fluentdConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type fluentdCollector struct {
	cfg    fluentdConfig
	client *http.Client
	url    string
}

func init() {
	Register("fluentd", func() Collector { return &fluentdCollector{} })
}

func (c *fluentdCollector) Name() string { return "fluentd" }

func (c *fluentdCollector) Configure(decode func(v any) error) error {
	if err := decode(&c.cfg); err != nil {
		return err
	}
	if c.cfg.Timeout <= 0 {
		c.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (c *fluentdCollector) Init(reg *registry.Registry) error {
	if c.cfg.Timeout <= 0 {
		if err := c.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	c.client = &http.Client{Timeout: c.cfg.Timeout}
	base := strings.TrimRight(c.cfg.URL, "/")
	if base == "" {
		base = "http://127.0.0.1:24220"
	}
	c.url = base
	if _, err := c.plugins(context.Background()); err != nil {
		return err
	}
	for _, ch := range []*registry.Chart{
		{ID: "fluentd.retry_count", Title: "Plugin Retry Count", Units: "count", Priority: 56000},
		{ID: "fluentd.buffer_queue_length", Title: "Plugin Buffer Queue Length", Units: "queue length", Priority: 56010},
		{ID: "fluentd.buffer_total_queued_size", Title: "Plugin Buffer Total Size", Units: "buffer total size", Priority: 56020},
	} {
		ch.Family, ch.Plugin, ch.Module = "fluentd", "fluentd", "fluentd"
		reg.AddChart(ch)
	}
	return nil
}

func (c *fluentdCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	plugins, err := c.plugins(ctx)
	if err != nil {
		return err
	}
	retry, queue, size := map[string]float64{}, map[string]float64{}, map[string]float64{}
	for _, p := range plugins {
		id := sanitizeID(p.ID)
		if id == "" {
			continue
		}
		if p.RetryCount != nil {
			ensureDim(reg, "fluentd.retry_count", id, &registry.Dimension{ID: id, Name: p.ID})
			retry[id] = float64(*p.RetryCount)
		}
		if p.BufferQueueLength != nil {
			ensureDim(reg, "fluentd.buffer_queue_length", id, &registry.Dimension{ID: id, Name: p.ID})
			queue[id] = float64(*p.BufferQueueLength)
		}
		if p.BufferTotalQueuedSize != nil {
			ensureDim(reg, "fluentd.buffer_total_queued_size", id, &registry.Dimension{ID: id, Name: p.ID})
			size[id] = float64(*p.BufferTotalQueuedSize)
		}
	}
	if len(retry) > 0 {
		_ = reg.Collect("fluentd.retry_count", now, retry)
	}
	if len(queue) > 0 {
		_ = reg.Collect("fluentd.buffer_queue_length", now, queue)
	}
	if len(size) > 0 {
		_ = reg.Collect("fluentd.buffer_total_queued_size", now, size)
	}
	return nil
}

type fluentdPlugin struct {
	ID                    string `json:"plugin_id"`
	Type                  string `json:"type"`
	Category              string `json:"plugin_category"`
	RetryCount            *int64 `json:"retry_count"`
	BufferQueueLength     *int64 `json:"buffer_queue_length"`
	BufferTotalQueuedSize *int64 `json:"buffer_total_queued_size"`
}

func (c *fluentdCollector) plugins(ctx context.Context) ([]fluentdPlugin, error) {
	b, err := httpGet(ctx, c.client, c.url+"/api/plugins.json")
	if err != nil {
		return nil, fmt.Errorf("fluentd: %w", err)
	}
	var wrap struct {
		Plugins []fluentdPlugin `json:"plugins"`
	}
	if err := json.Unmarshal(b, &wrap); err != nil {
		return nil, fmt.Errorf("fluentd: %w", err)
	}
	return wrap.Plugins, nil
}
