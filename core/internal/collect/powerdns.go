package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// powerdnsConfig is collectors.modules.powerdns (HTTP API statistics).
type powerdnsConfig struct {
	URL     string        `yaml:"url"`
	APIKey  string        `yaml:"api_key"`
	Timeout time.Duration `yaml:"timeout"`
}

type powerdnsCollector struct {
	cfg    powerdnsConfig
	client *http.Client
	url    string
}

func init() {
	Register("powerdns", func() Collector { return &powerdnsCollector{} })
}

func (p *powerdnsCollector) Name() string { return "powerdns" }

func (p *powerdnsCollector) Configure(decode func(v any) error) error {
	if err := decode(&p.cfg); err != nil {
		return err
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (p *powerdnsCollector) Init(reg *registry.Registry) error {
	if p.cfg.Timeout <= 0 {
		if err := p.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	p.client = &http.Client{Timeout: p.cfg.Timeout}
	base := strings.TrimRight(p.cfg.URL, "/")
	if base == "" {
		base = "http://127.0.0.1:8081"
	}
	p.url = base
	st, err := p.stats(context.Background())
	if err != nil {
		return err
	}
	if _, recursor := st["tcp-questions"]; recursor {
		return fmt.Errorf("powerdns: recursor metrics (use powerdns_recursor)")
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "powerdns.questions_in", Title: "Incoming questions", Units: "questions/s", Priority: 57000,
			Dimensions: []*registry.Dimension{{ID: "udp", Algorithm: inc}, {ID: "tcp", Algorithm: inc}}},
		{ID: "powerdns.questions_out", Title: "Outgoing questions", Units: "questions/s", Priority: 57010,
			Dimensions: []*registry.Dimension{{ID: "udp", Algorithm: inc}, {ID: "tcp", Algorithm: inc}}},
		{ID: "powerdns.cache_usage", Title: "Cache Usage", Units: "events/s", Priority: 57020,
			Dimensions: []*registry.Dimension{
				{ID: "query-cache-hit", Algorithm: inc}, {ID: "query-cache-miss", Algorithm: inc},
				{ID: "packet-cache-hit", Algorithm: inc}, {ID: "packet-cache-miss", Algorithm: inc},
			}},
		{ID: "powerdns.cache_size", Title: "Cache Size", Units: "entries", Priority: 57030,
			Dimensions: []*registry.Dimension{{ID: "query-cache"}, {ID: "packet-cache"}, {ID: "key-cache"}, {ID: "meta-cache"}}},
		{ID: "powerdns.latency", Title: "Answer latency", Units: "microseconds", Priority: 57040,
			Dimensions: []*registry.Dimension{{ID: "latency"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "powerdns", "powerdns", "powerdns"
		reg.AddChart(ch)
	}
	return nil
}

func (p *powerdnsCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	st, err := p.stats(ctx)
	if err != nil {
		return err
	}
	g := func(k string) float64 { return st[k] }
	_ = reg.Collect("powerdns.questions_in", now, map[string]float64{"udp": g("udp-queries"), "tcp": g("tcp-queries")})
	_ = reg.Collect("powerdns.questions_out", now, map[string]float64{"udp": g("udp-answers"), "tcp": g("tcp-answers")})
	_ = reg.Collect("powerdns.cache_usage", now, map[string]float64{
		"query-cache-hit": g("query-cache-hit"), "query-cache-miss": g("query-cache-miss"),
		"packet-cache-hit": g("packetcache-hit"), "packet-cache-miss": g("packetcache-miss"),
	})
	_ = reg.Collect("powerdns.cache_size", now, map[string]float64{
		"query-cache": g("query-cache-size"), "packet-cache": g("packetcache-size"),
		"key-cache": g("key-cache-size"), "meta-cache": g("meta-cache-size"),
	})
	_ = reg.Collect("powerdns.latency", now, map[string]float64{"latency": g("latency")})
	return nil
}

func (p *powerdnsCollector) stats(ctx context.Context) (map[string]float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url+"/api/v1/servers/localhost/statistics", nil)
	if err != nil {
		return nil, err
	}
	if p.cfg.APIKey != "" {
		req.Header.Set("X-API-Key", p.cfg.APIKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("powerdns: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("powerdns: %s", resp.Status)
	}
	var items []struct {
		Name  string `json:"name"`
		Type  string `json:"type"`
		Value any    `json:"value"`
	}
	if err := json.Unmarshal(b, &items); err != nil {
		return nil, fmt.Errorf("powerdns: %w", err)
	}
	out := map[string]float64{}
	for _, it := range items {
		if it.Type != "" && it.Type != "StatisticItem" {
			continue
		}
		switch v := it.Value.(type) {
		case string:
			out[it.Name] = firstFloat(v)
		case float64:
			out[it.Name] = v
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("powerdns: no statistics")
	}
	return out, nil
}
