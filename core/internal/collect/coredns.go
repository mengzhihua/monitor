package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// corednsConfig is collectors.modules.coredns (Prometheus /metrics).
type corednsConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type corednsCollector struct {
	cfg    corednsConfig
	client *http.Client
	url    string
}

func init() {
	Register("coredns", func() Collector { return &corednsCollector{} })
}

func (c *corednsCollector) Name() string { return "coredns" }

func (c *corednsCollector) Configure(decode func(v any) error) error {
	if err := decode(&c.cfg); err != nil {
		return err
	}
	if c.cfg.Timeout <= 0 {
		c.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (c *corednsCollector) Init(reg *registry.Registry) error {
	if c.cfg.Timeout <= 0 {
		if err := c.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	c.client = &http.Client{Timeout: c.cfg.Timeout}
	urls := []string{c.cfg.URL}
	if c.cfg.URL == "" {
		urls = []string{"http://127.0.0.1:9153/metrics"}
	}
	u, body, err := httpGetTry(context.Background(), c.client, urls)
	if err != nil {
		return err
	}
	if !strings.Contains(string(body), "coredns_") {
		return fmt.Errorf("coredns: no coredns_ metrics")
	}
	c.url = u
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "coredns.dns_request_count_total", Title: "CoreDNS DNS requests", Units: "requests/s", Priority: 51800,
			Dimensions: []*registry.Dimension{{ID: "requests", Algorithm: inc}}},
		{ID: "coredns.dns_requests_count_total_per_proto", Title: "CoreDNS requests per protocol", Units: "requests/s", Type: registry.Stacked, Priority: 51810,
			Dimensions: []*registry.Dimension{{ID: "udp", Algorithm: inc}, {ID: "tcp", Algorithm: inc}}},
		{ID: "coredns.dns_responses_count_total_per_rcode", Title: "CoreDNS responses per rcode", Units: "responses/s", Type: registry.Stacked, Priority: 51820,
			Dimensions: []*registry.Dimension{
				{ID: "NOERROR", Algorithm: inc}, {ID: "SERVFAIL", Algorithm: inc}, {ID: "NXDOMAIN", Algorithm: inc},
				{ID: "REFUSED", Algorithm: inc}, {ID: "other", Algorithm: inc}}},
		{ID: "coredns.dns_panic_count_total", Title: "Number Of Panics", Units: "panics/s", Priority: 51830,
			Dimensions: []*registry.Dimension{{ID: "panics", Algorithm: inc}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "coredns", "coredns", "coredns"
		reg.AddChart(ch)
	}
	return nil
}

func (c *corednsCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	b, err := httpGet(ctx, c.client, c.url)
	if err != nil {
		return err
	}
	samples := promSamples(string(b))
	reqNames := []string{"coredns_dns_requests_total", "coredns_dns_request_count_total"}
	rcodeNames := []string{"coredns_dns_responses_total", "coredns_dns_response_rcode_count_total"}
	proto := map[string]float64{"udp": 0, "tcp": 0}
	rcode := map[string]float64{"NOERROR": 0, "SERVFAIL": 0, "NXDOMAIN": 0, "REFUSED": 0, "other": 0}
	var req, panics float64
	for _, s := range samples {
		switch {
		case inNames(s.Name, reqNames):
			req += s.Value
			p := strings.ToLower(promLabel(s, "proto"))
			if p == "udp" || p == "tcp" {
				proto[p] += s.Value
			}
		case inNames(s.Name, rcodeNames):
			code := strings.ToUpper(promLabel(s, "rcode"))
			if _, ok := rcode[code]; ok && code != "other" {
				rcode[code] += s.Value
			} else {
				rcode["other"] += s.Value
			}
		case s.Name == "coredns_panics_total" || s.Name == "coredns_panic_count_total":
			panics += s.Value
		}
	}
	_ = reg.Collect("coredns.dns_request_count_total", now, map[string]float64{"requests": req})
	_ = reg.Collect("coredns.dns_requests_count_total_per_proto", now, proto)
	_ = reg.Collect("coredns.dns_responses_count_total_per_rcode", now, rcode)
	_ = reg.Collect("coredns.dns_panic_count_total", now, map[string]float64{"panics": panics})
	return nil
}

func inNames(name string, names []string) bool {
	for _, n := range names {
		if name == n {
			return true
		}
	}
	return false
}
