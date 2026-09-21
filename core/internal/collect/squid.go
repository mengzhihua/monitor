package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// squidConfig is collectors.modules.squid (cache manager counters).
type squidConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type squidCollector struct {
	cfg    squidConfig
	client *http.Client
	url    string
}

func init() {
	Register("squid", func() Collector { return &squidCollector{} })
}

func (s *squidCollector) Name() string { return "squid" }

func (s *squidCollector) Configure(decode func(v any) error) error {
	if err := decode(&s.cfg); err != nil {
		return err
	}
	if s.cfg.Timeout <= 0 {
		s.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (s *squidCollector) Init(reg *registry.Registry) error {
	if s.cfg.Timeout <= 0 {
		if err := s.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	s.client = &http.Client{Timeout: s.cfg.Timeout}
	urls := []string{s.cfg.URL}
	if s.cfg.URL == "" {
		urls = []string{
			"http://127.0.0.1:3128/squid-internal-mgr/counters",
			"http://127.0.0.1:3128/cache_object://localhost/counters",
		}
	}
	u, body, err := httpGetTry(context.Background(), s.client, urls)
	if err != nil {
		return err
	}
	if _, err := parseSquidCounters(string(body)); err != nil {
		return err
	}
	s.url = u
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "squid.clients_net", Title: "Squid Client Bandwidth", Units: "kilobits/s", Type: registry.Area, Priority: 51300,
			Dimensions: []*registry.Dimension{
				{ID: "in", Algorithm: inc, Multiplier: 8}, {ID: "out", Algorithm: inc, Multiplier: -8},
				{ID: "hits", Algorithm: inc, Multiplier: -8}}},
		{ID: "squid.clients_requests", Title: "Squid Client Requests", Units: "requests/s", Priority: 51310,
			Dimensions: []*registry.Dimension{{ID: "requests", Algorithm: inc}, {ID: "hits", Algorithm: inc}, {ID: "errors", Algorithm: inc, Multiplier: -1}}},
		{ID: "squid.servers_net", Title: "Squid Server Bandwidth", Units: "kilobits/s", Type: registry.Area, Priority: 51320,
			Dimensions: []*registry.Dimension{{ID: "in", Algorithm: inc, Multiplier: 8}, {ID: "out", Algorithm: inc, Multiplier: -8}}},
		{ID: "squid.servers_requests", Title: "Squid Server Requests", Units: "requests/s", Priority: 51330,
			Dimensions: []*registry.Dimension{{ID: "requests", Algorithm: inc}, {ID: "errors", Algorithm: inc, Multiplier: -1}}},
	} {
		c.Family, c.Plugin, c.Module = "squid", "squid", "squid"
		reg.AddChart(c)
	}
	return nil
}

func (s *squidCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	b, err := httpGet(ctx, s.client, s.url)
	if err != nil {
		return err
	}
	m, err := parseSquidCounters(string(b))
	if err != nil {
		return err
	}
	_ = reg.Collect("squid.clients_net", now, map[string]float64{
		"in": m["client_http.kbytes_in"], "out": m["client_http.kbytes_out"], "hits": m["client_http.hit_kbytes_out"]})
	_ = reg.Collect("squid.clients_requests", now, map[string]float64{
		"requests": m["client_http.requests"], "hits": m["client_http.hits"], "errors": m["client_http.errors"]})
	_ = reg.Collect("squid.servers_net", now, map[string]float64{"in": m["server.all.kbytes_in"], "out": m["server.all.kbytes_out"]})
	_ = reg.Collect("squid.servers_requests", now, map[string]float64{"requests": m["server.all.requests"], "errors": m["server.all.errors"]})
	return nil
}

func parseSquidCounters(s string) (map[string]float64, error) {
	out := map[string]float64{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			k, v, ok = strings.Cut(line, ":")
		}
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = firstFloat(v)
	}
	if _, ok := out["client_http.requests"]; !ok {
		return nil, fmt.Errorf("squid: no client_http.requests")
	}
	return out, nil
}
