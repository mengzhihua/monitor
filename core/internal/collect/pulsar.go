package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// pulsarConfig is collectors.modules.pulsar (Prometheus /metrics).
type pulsarConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type pulsarCollector struct {
	cfg    pulsarConfig
	client *http.Client
	url    string
}

func init() {
	Register("pulsar", func() Collector { return &pulsarCollector{} })
}

func (p *pulsarCollector) Name() string { return "pulsar" }

func (p *pulsarCollector) Configure(decode func(v any) error) error {
	if err := decode(&p.cfg); err != nil {
		return err
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (p *pulsarCollector) Init(reg *registry.Registry) error {
	if p.cfg.Timeout <= 0 {
		if err := p.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	p.client = &http.Client{Timeout: p.cfg.Timeout}
	urls := []string{p.cfg.URL}
	if p.cfg.URL == "" {
		urls = []string{"http://127.0.0.1:8080/metrics"}
	}
	u, body, err := httpGetTry(context.Background(), p.client, urls)
	if err != nil {
		return err
	}
	if !strings.Contains(string(body), "pulsar_") {
		return fmt.Errorf("pulsar: no pulsar_ metrics")
	}
	p.url = u
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "pulsar.broker_topics", Title: "Pulsar Broker Topics", Units: "topics", Priority: 55300,
			Dimensions: []*registry.Dimension{{ID: "topics"}}},
		{ID: "pulsar.broker_publish_rate", Title: "Pulsar Publish Rate", Units: "messages/s", Priority: 55310,
			Dimensions: []*registry.Dimension{{ID: "in", Algorithm: inc}}},
		{ID: "pulsar.broker_dispatch_rate", Title: "Pulsar Dispatch Rate", Units: "messages/s", Priority: 55320,
			Dimensions: []*registry.Dimension{{ID: "out", Algorithm: inc}}},
		{ID: "pulsar.broker_throughput", Title: "Pulsar Throughput", Units: "bytes/s", Type: registry.Area, Priority: 55330,
			Dimensions: []*registry.Dimension{{ID: "in", Algorithm: inc}, {ID: "out", Algorithm: inc, Multiplier: -1}}},
		{ID: "pulsar.subscription_backlog", Title: "Pulsar Subscription Backlog", Units: "messages", Priority: 55340,
			Dimensions: []*registry.Dimension{{ID: "backlog"}}},
	} {
		c.Family, c.Plugin, c.Module = "pulsar", "pulsar", "pulsar"
		reg.AddChart(c)
	}
	return nil
}

func (p *pulsarCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	b, err := httpGet(ctx, p.client, p.url)
	if err != nil {
		return err
	}
	samples := promSamples(string(b))
	_ = reg.Collect("pulsar.broker_topics", now, map[string]float64{
		"topics": promSumPrefixed(samples, "pulsar_topics_count", "pulsar_broker_topics_count")})
	_ = reg.Collect("pulsar.broker_publish_rate", now, map[string]float64{
		"in": promSumPrefixed(samples, "pulsar_rate_in", "pulsar_in_messages_total")})
	_ = reg.Collect("pulsar.broker_dispatch_rate", now, map[string]float64{
		"out": promSumPrefixed(samples, "pulsar_rate_out", "pulsar_out_messages_total")})
	_ = reg.Collect("pulsar.broker_throughput", now, map[string]float64{
		"in":  promSumPrefixed(samples, "pulsar_throughput_in", "pulsar_in_bytes_total"),
		"out": promSumPrefixed(samples, "pulsar_throughput_out", "pulsar_out_bytes_total")})
	_ = reg.Collect("pulsar.subscription_backlog", now, map[string]float64{
		"backlog": promSumPrefixed(samples, "pulsar_subscription_back_log", "pulsar_storage_backlog_size")})
	return nil
}
