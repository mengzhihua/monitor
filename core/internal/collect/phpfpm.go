package collect

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

type phpfpmConfig struct {
	URL     string        `yaml:"url"` // default http://127.0.0.1/status
	Timeout time.Duration `yaml:"timeout"`
}

type phpfpmCollector struct {
	cfg    phpfpmConfig
	client *http.Client
}

func init() {
	Register("phpfpm", func() Collector { return &phpfpmCollector{} })
}

func (p *phpfpmCollector) Name() string { return "phpfpm" }

func (p *phpfpmCollector) Configure(decode func(v any) error) error {
	if err := decode(&p.cfg); err != nil {
		return err
	}
	if p.cfg.URL == "" {
		p.cfg.URL = "http://127.0.0.1/status"
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (p *phpfpmCollector) Init(reg *registry.Registry) error {
	if p.cfg.URL == "" {
		if err := p.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	p.client = &http.Client{Timeout: p.cfg.Timeout}
	if _, err := p.fetch(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "phpfpm.performance", Title: "PHP-FPM active processes", Units: "processes", Type: registry.Stacked, Priority: 43000,
			Dimensions: []*registry.Dimension{{ID: "active"}, {ID: "idle"}}},
		{ID: "phpfpm.requests", Title: "PHP-FPM requests", Units: "requests/s", Priority: 43010,
			Dimensions: []*registry.Dimension{{ID: "requests", Algorithm: inc}}},
		{ID: "phpfpm.queue", Title: "PHP-FPM listen queue", Units: "connections", Priority: 43020,
			Dimensions: []*registry.Dimension{{ID: "queue"}, {ID: "max"}}},
		{ID: "phpfpm.slow", Title: "PHP-FPM slow requests", Units: "requests/s", Priority: 43030,
			Dimensions: []*registry.Dimension{{ID: "slow", Algorithm: inc}}},
	} {
		c.Family, c.Plugin, c.Module = "phpfpm", "phpfpm", "phpfpm"
		reg.AddChart(c)
	}
	return nil
}

func (p *phpfpmCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := p.fetch(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("phpfpm.performance", now, map[string]float64{"active": m["active processes"], "idle": m["idle processes"]})
	_ = reg.Collect("phpfpm.requests", now, map[string]float64{"requests": m["accepted conn"]})
	_ = reg.Collect("phpfpm.queue", now, map[string]float64{"queue": m["listen queue"], "max": m["max listen queue"]})
	_ = reg.Collect("phpfpm.slow", now, map[string]float64{"slow": m["slow requests"]})
	return nil
}

func (p *phpfpmCollector) fetch(ctx context.Context) (map[string]float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("phpfpm: %s -> %s", p.cfg.URL, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	return parsePHPFPMStatus(string(body))
}

func parsePHPFPMStatus(s string) (map[string]float64, error) {
	out := map[string]float64{}
	if !strings.Contains(s, "active processes") && !strings.Contains(s, "accepted conn") {
		return nil, fmt.Errorf("phpfpm: unexpected status body")
	}
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err == nil {
			out[strings.TrimSpace(k)] = n
		}
	}
	return out, nil
}
