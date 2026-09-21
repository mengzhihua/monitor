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

// powerdnsRecursorConfig is collectors.modules.powerdns_recursor.
type powerdnsRecursorConfig struct {
	URL     string        `yaml:"url"`
	APIKey  string        `yaml:"api_key"`
	Timeout time.Duration `yaml:"timeout"`
}

type powerdnsRecursorCollector struct {
	cfg    powerdnsRecursorConfig
	client *http.Client
	url    string
}

func init() {
	Register("powerdns_recursor", func() Collector { return &powerdnsRecursorCollector{} })
}

func (p *powerdnsRecursorCollector) Name() string { return "powerdns_recursor" }

func (p *powerdnsRecursorCollector) Configure(decode func(v any) error) error {
	if err := decode(&p.cfg); err != nil {
		return err
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (p *powerdnsRecursorCollector) Init(reg *registry.Registry) error {
	if p.cfg.Timeout <= 0 {
		if err := p.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	p.client = &http.Client{Timeout: p.cfg.Timeout}
	base := strings.TrimRight(p.cfg.URL, "/")
	if base == "" {
		base = "http://127.0.0.1:8082"
	}
	p.url = base
	st, err := p.stats(context.Background())
	if err != nil {
		return err
	}
	if _, auth := st["udp-queries"]; auth && st["tcp-questions"] == 0 && st["questions"] == 0 {
		return fmt.Errorf("powerdns_recursor: authoritative metrics (use powerdns)")
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "powerdns_recursor.questions_in", Title: "Incoming questions", Units: "questions/s", Priority: 58100,
			Dimensions: []*registry.Dimension{{ID: "total", Algorithm: inc}, {ID: "tcp", Algorithm: inc}, {ID: "ipv6", Algorithm: inc}}},
		{ID: "powerdns_recursor.questions_out", Title: "Outgoing questions", Units: "questions/s", Priority: 58110,
			Dimensions: []*registry.Dimension{{ID: "udp", Algorithm: inc}, {ID: "tcp", Algorithm: inc}, {ID: "ipv6", Algorithm: inc}, {ID: "throttled", Algorithm: inc}}},
		{ID: "powerdns_recursor.cache_usage", Title: "Cache Usage", Units: "events/s", Priority: 58120,
			Dimensions: []*registry.Dimension{
				{ID: "cache-hits", Algorithm: inc}, {ID: "cache-misses", Algorithm: inc},
				{ID: "packet-cache-hits", Algorithm: inc}, {ID: "packet-cache-misses", Algorithm: inc},
			}},
		{ID: "powerdns_recursor.drops", Title: "Drops", Units: "drops/s", Priority: 58130,
			Dimensions: []*registry.Dimension{{ID: "over-capacity-drops", Algorithm: inc}, {ID: "too-old-drops", Algorithm: inc}}},
		{ID: "powerdns_recursor.cache_size", Title: "Cache Size", Units: "entries", Priority: 58140,
			Dimensions: []*registry.Dimension{{ID: "cache"}, {ID: "packet-cache"}, {ID: "negative-cache"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "powerdns_recursor", "powerdns_recursor", "powerdns_recursor"
		reg.AddChart(ch)
	}
	return nil
}

func (p *powerdnsRecursorCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	st, err := p.stats(ctx)
	if err != nil {
		return err
	}
	g := func(k string) float64 { return st[k] }
	_ = reg.Collect("powerdns_recursor.questions_in", now, map[string]float64{"total": g("questions"), "tcp": g("tcp-questions"), "ipv6": g("ipv6-questions")})
	_ = reg.Collect("powerdns_recursor.questions_out", now, map[string]float64{
		"udp": g("all-outqueries"), "tcp": g("tcp-outqueries"), "ipv6": g("ipv6-outqueries"), "throttled": g("throttled-outqueries"),
	})
	_ = reg.Collect("powerdns_recursor.cache_usage", now, map[string]float64{
		"cache-hits": g("cache-hits"), "cache-misses": g("cache-misses"),
		"packet-cache-hits": g("packetcache-hits"), "packet-cache-misses": g("packetcache-misses"),
	})
	_ = reg.Collect("powerdns_recursor.drops", now, map[string]float64{"over-capacity-drops": g("over-capacity-drops"), "too-old-drops": g("too-old-drops")})
	_ = reg.Collect("powerdns_recursor.cache_size", now, map[string]float64{
		"cache": g("cache-entries"), "packet-cache": g("packetcache-entries"), "negative-cache": g("negcache-entries"),
	})
	return nil
}

func (p *powerdnsRecursorCollector) stats(ctx context.Context) (map[string]float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url+"/api/v1/servers/localhost/statistics", nil)
	if err != nil {
		return nil, err
	}
	if p.cfg.APIKey != "" {
		req.Header.Set("X-API-Key", p.cfg.APIKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("powerdns_recursor: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("powerdns_recursor: %s", resp.Status)
	}
	var items []struct {
		Name  string `json:"name"`
		Type  string `json:"type"`
		Value any    `json:"value"`
	}
	if err := json.Unmarshal(b, &items); err != nil {
		return nil, fmt.Errorf("powerdns_recursor: %w", err)
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
		return nil, fmt.Errorf("powerdns_recursor: no statistics")
	}
	return out, nil
}
