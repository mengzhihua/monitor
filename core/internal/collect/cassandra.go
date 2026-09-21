package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/ingest"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

// cassandraConfig is collectors.modules.cassandra (JMX Prometheus :7072).
type cassandraConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type cassandraCollector struct {
	cfg    cassandraConfig
	client *http.Client
	url    string
}

func init() {
	Register("cassandra", func() Collector { return &cassandraCollector{} })
}

func (c *cassandraCollector) Name() string { return "cassandra" }

func (c *cassandraCollector) Configure(decode func(v any) error) error {
	if err := decode(&c.cfg); err != nil {
		return err
	}
	if c.cfg.Timeout <= 0 {
		c.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (c *cassandraCollector) Init(reg *registry.Registry) error {
	if c.cfg.Timeout <= 0 {
		if err := c.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	c.client = &http.Client{Timeout: c.cfg.Timeout}
	urls := []string{c.cfg.URL}
	if c.cfg.URL == "" {
		urls = []string{"http://127.0.0.1:7072/metrics", "http://127.0.0.1:7072/"}
	}
	u, body, err := httpGetTry(context.Background(), c.client, urls)
	if err != nil {
		return err
	}
	c.url = u
	samples := promSamples(string(body))
	if !cassandraIsMetrics(samples) {
		return fmt.Errorf("cassandra: not cassandra metrics")
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "cassandra.client_requests_rate", Title: "Client requests rate", Units: "requests/s", Priority: 56200,
			Dimensions: []*registry.Dimension{{ID: "read", Algorithm: inc}, {ID: "write", Algorithm: inc, Multiplier: -1}}},
		{ID: "cassandra.jvm_memory_used", Title: "Memory used", Units: "bytes", Type: registry.Stacked, Priority: 56210,
			Dimensions: []*registry.Dimension{{ID: "heap"}, {ID: "nonheap"}}},
		{ID: "cassandra.dropped_messages_rate", Title: "Dropped messages rate", Units: "messages/s", Priority: 56220,
			Dimensions: []*registry.Dimension{{ID: "dropped"}}},
		{ID: "cassandra.storage_live_disk_space_used", Title: "Disk space used by live data", Units: "bytes", Priority: 56230,
			Dimensions: []*registry.Dimension{{ID: "used"}}},
		{ID: "cassandra.compaction_pending_tasks_count", Title: "Pending compactions", Units: "tasks", Priority: 56240,
			Dimensions: []*registry.Dimension{{ID: "pending"}}},
		{ID: "cassandra.client_requests_failures_rate", Title: "Client requests failures rate", Units: "failures/s", Priority: 56250,
			Dimensions: []*registry.Dimension{{ID: "read", Algorithm: inc}, {ID: "write", Algorithm: inc, Multiplier: -1}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "cassandra", "cassandra", "cassandra"
		reg.AddChart(ch)
	}
	return nil
}

func (c *cassandraCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	b, err := httpGet(ctx, c.client, c.url)
	if err != nil {
		return fmt.Errorf("cassandra: %w", err)
	}
	samples := promSamples(string(b))
	read, write := cassandraClient(samples, "Latency")
	_ = reg.Collect("cassandra.client_requests_rate", now, map[string]float64{"read": read, "write": write})
	fr, fw := cassandraClient(samples, "Failures")
	_ = reg.Collect("cassandra.client_requests_failures_rate", now, map[string]float64{"read": fr, "write": fw})
	var heap, nonheap float64
	for _, s := range samples {
		if s.Name == "jvm_memory_bytes_used" {
			switch promLabel(s, "area") {
			case "heap":
				heap += s.Value
			case "nonheap":
				nonheap += s.Value
			}
		}
	}
	_ = reg.Collect("cassandra.jvm_memory_used", now, map[string]float64{"heap": heap, "nonheap": nonheap})
	_ = reg.Collect("cassandra.dropped_messages_rate", now, map[string]float64{"dropped": promSum(samples, "org_apache_cassandra_metrics_droppedmessage_count")})
	_ = reg.Collect("cassandra.storage_live_disk_space_used", now, map[string]float64{"used": cassandraNamed(samples, "org_apache_cassandra_metrics_storage_count", "Load")})
	_ = reg.Collect("cassandra.compaction_pending_tasks_count", now, map[string]float64{"pending": cassandraNamed(samples, "org_apache_cassandra_metrics_compaction_value", "PendingTasks")})
	return nil
}

func cassandraIsMetrics(samples []ingest.Sample) bool {
	for _, s := range samples {
		if strings.HasPrefix(s.Name, "org_apache_cassandra_metrics") {
			return true
		}
	}
	return false
}

func cassandraClient(samples []ingest.Sample, name string) (read, write float64) {
	for _, s := range samples {
		if s.Name != "org_apache_cassandra_metrics_clientrequest_count" {
			continue
		}
		if promLabel(s, "name") != name {
			continue
		}
		switch promLabel(s, "scope") {
		case "Read":
			read += s.Value
		case "Write":
			write += s.Value
		}
	}
	return read, write
}

func cassandraNamed(samples []ingest.Sample, metric, name string) float64 {
	var n float64
	for _, s := range samples {
		if s.Name == metric && promLabel(s, "name") == name {
			n += s.Value
		}
	}
	return n
}
