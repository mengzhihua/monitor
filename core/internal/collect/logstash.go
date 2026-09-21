package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// logstashConfig is collectors.modules.logstash (HTTP /_node/stats).
type logstashConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type logstashCollector struct {
	cfg    logstashConfig
	client *http.Client
	url    string
}

func init() {
	Register("logstash", func() Collector { return &logstashCollector{} })
}

func (c *logstashCollector) Name() string { return "logstash" }

func (c *logstashCollector) Configure(decode func(v any) error) error {
	if err := decode(&c.cfg); err != nil {
		return err
	}
	if c.cfg.Timeout <= 0 {
		c.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (c *logstashCollector) Init(reg *registry.Registry) error {
	if c.cfg.Timeout <= 0 {
		if err := c.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	c.client = &http.Client{Timeout: c.cfg.Timeout}
	base := strings.TrimRight(c.cfg.URL, "/")
	if base == "" {
		base = "http://127.0.0.1:9600"
	}
	c.url = base
	if _, err := c.stats(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "logstash.jvm_threads", Title: "JVM Threads", Units: "count", Priority: 56100,
			Dimensions: []*registry.Dimension{{ID: "threads"}}},
		{ID: "logstash.jvm_mem_heap_used", Title: "JVM Heap Memory Percentage", Units: "percentage", Priority: 56110,
			Dimensions: []*registry.Dimension{{ID: "in_use", Name: "in use"}}},
		{ID: "logstash.jvm_mem_heap", Title: "JVM Heap Memory", Units: "KiB", Type: registry.Area, Priority: 56120,
			Dimensions: []*registry.Dimension{{ID: "committed", Divisor: 1024}, {ID: "used", Divisor: 1024}}},
		{ID: "logstash.open_file_descriptors", Title: "Open File Descriptors", Units: "fd", Priority: 56130,
			Dimensions: []*registry.Dimension{{ID: "open"}}},
		{ID: "logstash.event", Title: "Events Overview", Units: "events/s", Priority: 56140,
			Dimensions: []*registry.Dimension{{ID: "in", Algorithm: inc}, {ID: "filtered", Algorithm: inc}, {ID: "out", Algorithm: inc}}},
		{ID: "logstash.uptime", Title: "Uptime", Units: "seconds", Priority: 56150,
			Dimensions: []*registry.Dimension{{ID: "uptime", Divisor: 1000}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "logstash", "logstash", "logstash"
		reg.AddChart(ch)
	}
	return nil
}

func (c *logstashCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := c.stats(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("logstash.jvm_threads", now, map[string]float64{"threads": nestFloat(m, "jvm", "threads", "count")})
	_ = reg.Collect("logstash.jvm_mem_heap_used", now, map[string]float64{"in_use": nestFloat(m, "jvm", "mem", "heap_used_percent")})
	_ = reg.Collect("logstash.jvm_mem_heap", now, map[string]float64{
		"committed": nestFloat(m, "jvm", "mem", "heap_committed_in_bytes"),
		"used":      nestFloat(m, "jvm", "mem", "heap_used_in_bytes"),
	})
	_ = reg.Collect("logstash.open_file_descriptors", now, map[string]float64{"open": nestFloat(m, "process", "open_file_descriptors")})
	_ = reg.Collect("logstash.event", now, map[string]float64{
		"in":       nestFloat(m, "events", "in"),
		"filtered": nestFloat(m, "events", "filtered"),
		"out":      nestFloat(m, "events", "out"),
	})
	_ = reg.Collect("logstash.uptime", now, map[string]float64{"uptime": nestFloat(m, "jvm", "uptime_in_millis")})
	return nil
}

func (c *logstashCollector) stats(ctx context.Context) (map[string]any, error) {
	b, err := httpGet(ctx, c.client, c.url+"/_node/stats")
	if err != nil {
		return nil, fmt.Errorf("logstash: %w", err)
	}
	m, err := jsonMap(b)
	if err != nil {
		return nil, fmt.Errorf("logstash: %w", err)
	}
	if nestMap(m, "jvm") == nil && nestMap(m, "events") == nil {
		return nil, fmt.Errorf("logstash: no stats")
	}
	return m, nil
}
