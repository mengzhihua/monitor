package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// traefikConfig is collectors.modules.traefik (Prometheus /metrics).
type traefikConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type traefikCollector struct {
	cfg    traefikConfig
	client *http.Client
	url    string
}

func init() {
	Register("traefik", func() Collector { return &traefikCollector{} })
}

func (t *traefikCollector) Name() string { return "traefik" }

func (t *traefikCollector) Configure(decode func(v any) error) error {
	if err := decode(&t.cfg); err != nil {
		return err
	}
	if t.cfg.Timeout <= 0 {
		t.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (t *traefikCollector) Init(reg *registry.Registry) error {
	if t.cfg.Timeout <= 0 {
		if err := t.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	t.client = &http.Client{Timeout: t.cfg.Timeout}
	urls := []string{t.cfg.URL}
	if t.cfg.URL == "" {
		urls = []string{"http://127.0.0.1:8082/metrics", "http://127.0.0.1:8080/metrics"}
	}
	u, body, err := httpGetTry(context.Background(), t.client, urls)
	if err != nil {
		return err
	}
	if !strings.Contains(string(body), "traefik_") {
		return fmt.Errorf("traefik: no traefik_ metrics")
	}
	t.url = u
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "traefik.entrypoint_requests", Title: "Traefik entrypoint requests", Units: "requests/s", Type: registry.Stacked, Priority: 51500,
			Dimensions: []*registry.Dimension{
				{ID: "1xx", Algorithm: inc}, {ID: "2xx", Algorithm: inc}, {ID: "3xx", Algorithm: inc},
				{ID: "4xx", Algorithm: inc}, {ID: "5xx", Algorithm: inc}}},
		{ID: "traefik.entrypoint_open_connections", Title: "Traefik open connections", Units: "connections", Priority: 51510,
			Dimensions: []*registry.Dimension{{ID: "open"}}},
	} {
		c.Family, c.Plugin, c.Module = "traefik", "traefik", "traefik"
		reg.AddChart(c)
	}
	return nil
}

func (t *traefikCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	b, err := httpGet(ctx, t.client, t.url)
	if err != nil {
		return err
	}
	samples := promSamples(string(b))
	cls := map[string]float64{"1xx": 0, "2xx": 0, "3xx": 0, "4xx": 0, "5xx": 0}
	open := 0.0
	for _, s := range samples {
		switch s.Name {
		case "traefik_entrypoint_requests_total", "traefik_entrypoint_request_total":
			cls[httpStatusClass(promLabel(s, "code"))] += s.Value
		case "traefik_entrypoint_open_connections":
			open += s.Value
		}
	}
	_ = reg.Collect("traefik.entrypoint_requests", now, cls)
	_ = reg.Collect("traefik.entrypoint_open_connections", now, map[string]float64{"open": open})
	return nil
}
