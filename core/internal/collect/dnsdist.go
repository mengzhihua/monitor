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

// dnsdistConfig is collectors.modules.dnsdist (HTTP jsonstat).
type dnsdistConfig struct {
	URL     string        `yaml:"url"`
	APIKey  string        `yaml:"api_key"`
	Timeout time.Duration `yaml:"timeout"`
}

type dnsdistCollector struct {
	cfg    dnsdistConfig
	client *http.Client
	url    string
}

func init() {
	Register("dnsdist", func() Collector { return &dnsdistCollector{} })
}

func (d *dnsdistCollector) Name() string { return "dnsdist" }

func (d *dnsdistCollector) Configure(decode func(v any) error) error {
	if err := decode(&d.cfg); err != nil {
		return err
	}
	if d.cfg.Timeout <= 0 {
		d.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (d *dnsdistCollector) Init(reg *registry.Registry) error {
	if d.cfg.Timeout <= 0 {
		if err := d.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	d.client = &http.Client{Timeout: d.cfg.Timeout}
	base := strings.TrimRight(d.cfg.URL, "/")
	if base == "" {
		base = "http://127.0.0.1:8083"
	}
	d.url = base
	if _, err := d.stats(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "dnsdist.queries", Title: "Client queries received", Units: "queries/s", Priority: 58800,
			Dimensions: []*registry.Dimension{{ID: "all", Algorithm: inc}, {ID: "recursive", Algorithm: inc}, {ID: "empty", Algorithm: inc}}},
		{ID: "dnsdist.queries_dropped", Title: "Client queries dropped", Units: "queries/s", Priority: 58810,
			Dimensions: []*registry.Dimension{
				{ID: "rule_drop", Algorithm: inc}, {ID: "dynamic_blocked", Algorithm: inc},
				{ID: "no_policy", Algorithm: inc}, {ID: "non_queries", Algorithm: inc},
			}},
		{ID: "dnsdist.answers", Title: "Answers statistics", Units: "answers/s", Priority: 58820,
			Dimensions: []*registry.Dimension{{ID: "self_answered", Algorithm: inc}, {ID: "nxdomain", Algorithm: inc, Multiplier: -1}, {ID: "refused", Algorithm: inc, Multiplier: -1}}},
		{ID: "dnsdist.cache", Title: "Cache performance", Units: "answers/s", Priority: 58830,
			Dimensions: []*registry.Dimension{{ID: "hits", Algorithm: inc}, {ID: "misses", Algorithm: inc, Multiplier: -1}}},
		{ID: "dnsdist.backend_errors", Title: "Backend error responses", Units: "responses/s", Priority: 58840,
			Dimensions: []*registry.Dimension{{ID: "timeouts", Algorithm: inc}, {ID: "servfail", Algorithm: inc}}},
		{ID: "dnsdist.servermem", Title: "DNSdist server memory utilization", Units: "MiB", Type: registry.Area, Priority: 58850,
			Dimensions: []*registry.Dimension{{ID: "memory_usage", Divisor: 1 << 20}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "dnsdist", "dnsdist", "dnsdist"
		reg.AddChart(ch)
	}
	return nil
}

func (d *dnsdistCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	st, err := d.stats(ctx)
	if err != nil {
		return err
	}
	g := func(k string) float64 { return st[k] }
	_ = reg.Collect("dnsdist.queries", now, map[string]float64{"all": g("queries"), "recursive": g("rdqueries"), "empty": g("empty-queries")})
	_ = reg.Collect("dnsdist.queries_dropped", now, map[string]float64{
		"rule_drop": g("rule-drop"), "dynamic_blocked": g("dyn-blocked"), "no_policy": g("no-policy"), "non_queries": g("noncompliant-queries"),
	})
	_ = reg.Collect("dnsdist.answers", now, map[string]float64{"self_answered": g("self-answered"), "nxdomain": g("rule-nxdomain"), "refused": g("rule-refused")})
	_ = reg.Collect("dnsdist.cache", now, map[string]float64{"hits": g("cache-hits"), "misses": g("cache-misses")})
	_ = reg.Collect("dnsdist.backend_errors", now, map[string]float64{"timeouts": g("downstream-timeouts"), "servfail": g("servfail-responses")})
	_ = reg.Collect("dnsdist.servermem", now, map[string]float64{"memory_usage": g("real-memory-usage")})
	return nil
}

func (d *dnsdistCollector) stats(ctx context.Context) (map[string]float64, error) {
	urls := []string{d.url + "/jsonstat?command=stats", d.url + "/api/v1/servers/localhost/statistics", d.url}
	var last error
	for _, u := range urls {
		b, err := d.get(ctx, u)
		if err != nil {
			last = err
			continue
		}
		st, err := parseDNSDist(b)
		if err != nil {
			last = err
			continue
		}
		if _, ok := st["queries"]; ok {
			return st, nil
		}
		last = fmt.Errorf("unexpected stats")
	}
	if last == nil {
		last = fmt.Errorf("no stats")
	}
	return nil, fmt.Errorf("dnsdist: %w", last)
}

func (d *dnsdistCollector) get(ctx context.Context, url string) ([]byte, error) {
	if d.cfg.APIKey == "" {
		return httpGet(ctx, d.client, url)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", d.cfg.APIKey)
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return b, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return b, nil
}

func parseDNSDist(b []byte) (map[string]float64, error) {
	out := map[string]float64{}
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		return nil, err
	}
	if _, ok := obj["queries"]; ok {
		for k, v := range obj {
			out[k] = nestFloat(v)
		}
		return out, nil
	}
	// API array form [{name,value}]
	var items []struct {
		Name  string `json:"name"`
		Value any    `json:"value"`
	}
	if err := json.Unmarshal(b, &items); err == nil {
		for _, it := range items {
			out[it.Name] = nestFloat(it.Value)
		}
	}
	return out, nil
}
