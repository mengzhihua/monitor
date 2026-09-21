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

// vernemqConfig is collectors.modules.vernemq (Prometheus :8888/metrics).
type vernemqConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type vernemqCollector struct {
	cfg    vernemqConfig
	client *http.Client
	url    string
}

func init() {
	Register("vernemq", func() Collector { return &vernemqCollector{} })
}

func (v *vernemqCollector) Name() string { return "vernemq" }

func (v *vernemqCollector) Configure(decode func(v any) error) error {
	if err := decode(&v.cfg); err != nil {
		return err
	}
	if v.cfg.Timeout <= 0 {
		v.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (v *vernemqCollector) Init(reg *registry.Registry) error {
	if v.cfg.Timeout <= 0 {
		if err := v.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	v.client = &http.Client{Timeout: v.cfg.Timeout}
	urls := []string{v.cfg.URL}
	if v.cfg.URL == "" {
		urls = []string{"http://127.0.0.1:8888/metrics", "http://127.0.0.1:8888/"}
	}
	u, body, err := httpGetTry(context.Background(), v.client, urls)
	if err != nil {
		return err
	}
	v.url = u
	if !vernemqIsMetrics(string(body)) {
		return fmt.Errorf("vernemq: not vernemq metrics")
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "vernemq.node_sockets", Title: "Open Sockets", Units: "sockets", Priority: 59500,
			Dimensions: []*registry.Dimension{{ID: "open"}}},
		{ID: "vernemq.node_socket_operations", Title: "Open and Close Socket Events", Units: "events/s", Priority: 59510,
			Dimensions: []*registry.Dimension{{ID: "open", Algorithm: inc}, {ID: "close", Algorithm: inc, Multiplier: -1}}},
		{ID: "vernemq.node_queue_messages", Title: "Queue Messages", Units: "messages/s", Priority: 59520,
			Dimensions: []*registry.Dimension{{ID: "in", Algorithm: inc}, {ID: "out", Algorithm: inc, Multiplier: -1}}},
		{ID: "vernemq.node_mqtt_publish", Title: "MQTT Publish Packets", Units: "packets/s", Priority: 59530,
			Dimensions: []*registry.Dimension{{ID: "received", Algorithm: inc}, {ID: "sent", Algorithm: inc, Multiplier: -1}}},
		{ID: "vernemq.node_traffic", Title: "Node Traffic", Units: "bytes/s", Type: registry.Area, Priority: 59540,
			Dimensions: []*registry.Dimension{{ID: "received", Algorithm: inc}, {ID: "sent", Algorithm: inc, Multiplier: -1}}},
		{ID: "vernemq.node_uptime", Title: "Uptime", Units: "seconds", Priority: 59550,
			Dimensions: []*registry.Dimension{{ID: "uptime"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "vernemq", "vernemq", "vernemq"
		reg.AddChart(ch)
	}
	return nil
}

func (v *vernemqCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	b, err := httpGet(ctx, v.client, v.url)
	if err != nil {
		return fmt.Errorf("vernemq: %w", err)
	}
	samples := promSamples(string(b))
	_ = reg.Collect("vernemq.node_sockets", now, map[string]float64{"open": vernemqSum(samples, "open_sockets", "socket_open_count")})
	_ = reg.Collect("vernemq.node_socket_operations", now, map[string]float64{
		"open":  vernemqSum(samples, "socket_open"),
		"close": vernemqSum(samples, "socket_close"),
	})
	_ = reg.Collect("vernemq.node_queue_messages", now, map[string]float64{
		"in":  vernemqSum(samples, "queue_message_in", "queue_in"),
		"out": vernemqSum(samples, "queue_message_out", "queue_out"),
	})
	_ = reg.Collect("vernemq.node_mqtt_publish", now, map[string]float64{
		"received": vernemqSum(samples, "mqtt_publish_received", "mqtt_PUBLISH_received"),
		"sent":     vernemqSum(samples, "mqtt_publish_sent", "mqtt_PUBLISH_sent"),
	})
	_ = reg.Collect("vernemq.node_traffic", now, map[string]float64{
		"received": vernemqSum(samples, "bytes_received"),
		"sent":     vernemqSum(samples, "bytes_sent"),
	})
	_ = reg.Collect("vernemq.node_uptime", now, map[string]float64{"uptime": vernemqSum(samples, "uptime", "system_wall_clock")})
	return nil
}

func vernemqIsMetrics(body string) bool {
	return strings.Contains(body, "vernemq") || strings.Contains(body, "vmq_") || strings.Contains(body, "open_sockets")
}

func vernemqSum(samples []ingest.Sample, suffixes ...string) float64 {
	var n float64
	for _, s := range samples {
		for _, suf := range suffixes {
			if s.Name == suf || strings.HasSuffix(s.Name, "_"+suf) || strings.Contains(s.Name, suf) {
				n += s.Value
				break
			}
		}
	}
	return n
}
