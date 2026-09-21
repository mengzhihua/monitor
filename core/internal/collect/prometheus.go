package collect

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mengzhihua/monitor/core/internal/ingest"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

// prometheusConfig is collectors.modules.prometheus — scrape jobs, matching
// Netdata's go.d prometheus collector (any OpenMetrics / Prometheus endpoint).
type prometheusConfig struct {
	Jobs    []prometheusJob `yaml:"jobs"`
	Timeout time.Duration   `yaml:"timeout"`
}

type prometheusJob struct {
	Name    string            `yaml:"name"`
	URL     string            `yaml:"url"`
	Timeout time.Duration     `yaml:"timeout"`
	Headers map[string]string `yaml:"headers"`
}

type prometheusCollector struct {
	cfg    prometheusConfig
	client *http.Client
	maps   map[string]*ingest.Mapper
}

func init() {
	Register("prometheus", func() Collector { return &prometheusCollector{} })
}

func (p *prometheusCollector) Name() string { return "prometheus" }

func (p *prometheusCollector) Configure(decode func(v any) error) error {
	if err := decode(&p.cfg); err != nil {
		return err
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (p *prometheusCollector) Init(_ *registry.Registry) error {
	if p.cfg.Timeout <= 0 {
		if err := p.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if len(p.cfg.Jobs) == 0 {
		return fmt.Errorf("no jobs configured")
	}
	p.client = &http.Client{Timeout: p.cfg.Timeout}
	p.maps = map[string]*ingest.Mapper{}
	for i := range p.cfg.Jobs {
		j := &p.cfg.Jobs[i]
		if j.Name == "" {
			j.Name = fmt.Sprintf("job%d", i)
		}
		if j.URL == "" {
			return fmt.Errorf("prometheus job %q: empty url", j.Name)
		}
		if j.Timeout <= 0 {
			j.Timeout = p.cfg.Timeout
		}
		p.maps[j.Name] = ingest.NewMapper(ingest.Options{
			Prefix: "prom", Plugin: "prometheus", Module: j.Name, Family: "prometheus/" + j.Name,
		})
	}
	return nil
}

func (p *prometheusCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	var last error
	n := 0
	for _, j := range p.cfg.Jobs {
		body, err := p.fetch(ctx, j)
		if err != nil {
			last = err
			continue
		}
		samples := ingest.ParseOpenMetrics(string(body))
		n += p.maps[j.Name].Apply(reg, now, samples)
	}
	if n == 0 && last != nil {
		return last
	}
	return nil
}

func (p *prometheusCollector) fetch(ctx context.Context, j prometheusJob) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/openmetrics-text;version=1.0.0,text/plain;version=0.0.4;q=0.9,*/*;q=0.8")
	for k, v := range j.Headers {
		req.Header.Set(k, v)
	}
	c := p.client
	if j.Timeout > 0 && j.Timeout != p.cfg.Timeout {
		c = &http.Client{Timeout: j.Timeout}
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus %s: %s", j.Name, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}
