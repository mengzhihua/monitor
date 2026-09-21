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

// dockerEngineConfig is collectors.modules.docker_engine (Prometheus :9323).
type dockerEngineConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type dockerEngineCollector struct {
	cfg    dockerEngineConfig
	client *http.Client
	url    string
}

func init() {
	Register("docker_engine", func() Collector { return &dockerEngineCollector{} })
}

func (d *dockerEngineCollector) Name() string { return "docker_engine" }

func (d *dockerEngineCollector) Configure(decode func(v any) error) error {
	if err := decode(&d.cfg); err != nil {
		return err
	}
	if d.cfg.Timeout <= 0 {
		d.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (d *dockerEngineCollector) Init(reg *registry.Registry) error {
	if d.cfg.Timeout <= 0 {
		if err := d.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	d.client = &http.Client{Timeout: d.cfg.Timeout}
	urls := []string{d.cfg.URL}
	if d.cfg.URL == "" {
		urls = []string{"http://127.0.0.1:9323/metrics", "http://127.0.0.1:9323/"}
	}
	u, body, err := httpGetTry(context.Background(), d.client, urls)
	if err != nil {
		return err
	}
	d.url = u
	if !dockerEngineIsMetrics(string(body)) {
		return fmt.Errorf("docker_engine: not docker engine metrics")
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "docker_engine.engine_daemon_container_actions", Title: "Container Actions", Units: "actions/s", Type: registry.Stacked, Priority: 60200,
			Dimensions: []*registry.Dimension{
				{ID: "changes", Algorithm: inc}, {ID: "commit", Algorithm: inc}, {ID: "create", Algorithm: inc},
				{ID: "delete", Algorithm: inc}, {ID: "start", Algorithm: inc},
			}},
		{ID: "docker_engine.engine_daemon_container_states_containers", Title: "Containers In Various States", Units: "containers", Type: registry.Stacked, Priority: 60210,
			Dimensions: []*registry.Dimension{{ID: "running"}, {ID: "paused"}, {ID: "stopped"}}},
		{ID: "docker_engine.engine_daemon_health_checks_failed_total", Title: "Health Checks", Units: "events/s", Priority: 60220,
			Dimensions: []*registry.Dimension{{ID: "fails", Algorithm: inc}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "docker_engine", "docker_engine", "docker_engine"
		reg.AddChart(ch)
	}
	return nil
}

func (d *dockerEngineCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	b, err := httpGet(ctx, d.client, d.url)
	if err != nil {
		return fmt.Errorf("docker_engine: %w", err)
	}
	samples := promSamples(string(b))
	act := dockerEngineByLabel(samples, "engine_daemon_container_actions_seconds_count", "action")
	if len(act) == 0 {
		act = dockerEngineByLabel(samples, "engine_daemon_container_actions_seconds", "action")
	}
	_ = reg.Collect("docker_engine.engine_daemon_container_actions", now, map[string]float64{
		"changes": act["changes"], "commit": act["commit"], "create": act["create"], "delete": act["delete"], "start": act["start"],
	})
	st := dockerEngineByLabel(samples, "engine_daemon_container_states_containers", "state")
	_ = reg.Collect("docker_engine.engine_daemon_container_states_containers", now, map[string]float64{
		"running": st["running"], "paused": st["paused"], "stopped": st["stopped"],
	})
	_ = reg.Collect("docker_engine.engine_daemon_health_checks_failed_total", now, map[string]float64{
		"fails": promSum(samples, "engine_daemon_health_checks_failed_total"),
	})
	return nil
}

func dockerEngineIsMetrics(body string) bool {
	return strings.Contains(body, "engine_daemon_") || strings.Contains(body, "builder_builds")
}

func dockerEngineByLabel(samples []ingest.Sample, name, label string) map[string]float64 {
	return promSumByLabel(samples, name, label)
}
