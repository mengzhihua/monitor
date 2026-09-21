package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// piholeConfig is collectors.modules.pihole (FTL / admin API).
type piholeConfig struct {
	URL     string        `yaml:"url"`
	Token   string        `yaml:"token"`
	Timeout time.Duration `yaml:"timeout"`
}

type piholeCollector struct {
	cfg    piholeConfig
	client *http.Client
	url    string
}

func init() {
	Register("pihole", func() Collector { return &piholeCollector{} })
}

func (p *piholeCollector) Name() string { return "pihole" }

func (p *piholeCollector) Configure(decode func(v any) error) error {
	if err := decode(&p.cfg); err != nil {
		return err
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (p *piholeCollector) Init(reg *registry.Registry) error {
	if p.cfg.Timeout <= 0 {
		if err := p.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	p.client = &http.Client{Timeout: p.cfg.Timeout}
	base := strings.TrimRight(p.cfg.URL, "/")
	if base == "" {
		base = "http://127.0.0.1"
	}
	p.url = base
	if _, err := p.summary(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "pihole.dns_queries_total", Title: "Pi-hole DNS Queries Total (Cached, Blocked and Forwarded)", Units: "queries/s", Priority: 58000,
			Dimensions: []*registry.Dimension{{ID: "queries", Algorithm: inc}}},
		{ID: "pihole.dns_queries_blocked_percent", Title: "Pi-hole DNS Queries Blocked Percent", Units: "percent", Priority: 58010,
			Dimensions: []*registry.Dimension{{ID: "blocked"}}},
		{ID: "pihole.dns_queries_by_destination", Title: "Pi-hole DNS Queries by Destination", Units: "queries/s", Type: registry.Stacked, Priority: 58020,
			Dimensions: []*registry.Dimension{{ID: "cached", Algorithm: inc}, {ID: "blocked", Algorithm: inc}, {ID: "forwarded", Algorithm: inc}}},
		{ID: "pihole.active_clients", Title: "Pi-hole Active Clients (Seen in the Last 24 Hours)", Units: "clients", Priority: 58030,
			Dimensions: []*registry.Dimension{{ID: "active"}}},
		{ID: "pihole.gravity_list_blocked_domains", Title: "Pi-hole Gravity List Blocked Domains", Units: "domains", Priority: 58040,
			Dimensions: []*registry.Dimension{{ID: "blocked"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "pihole", "pihole", "pihole"
		reg.AddChart(ch)
	}
	return nil
}

func (p *piholeCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := p.summary(ctx)
	if err != nil {
		return err
	}
	queries := nestFloat(m, "dns_queries_today")
	if queries == 0 {
		queries = nestFloat(m, "queries", "total")
	}
	blocked := nestFloat(m, "ads_blocked_today")
	if blocked == 0 {
		blocked = nestFloat(m, "queries", "blocked")
	}
	pct := nestFloat(m, "ads_percentage_today")
	if pct == 0 && queries > 0 {
		pct = blocked * 100 / queries
	}
	cached := nestFloat(m, "queries_cached")
	fwd := nestFloat(m, "queries_forwarded")
	clients := nestFloat(m, "unique_clients")
	if clients == 0 {
		clients = nestFloat(m, "clients", "active")
	}
	domains := nestFloat(m, "domains_being_blocked")
	if domains == 0 {
		domains = nestFloat(m, "gravity", "domains_being_blocked")
	}
	_ = reg.Collect("pihole.dns_queries_total", now, map[string]float64{"queries": queries})
	_ = reg.Collect("pihole.dns_queries_blocked_percent", now, map[string]float64{"blocked": pct})
	_ = reg.Collect("pihole.dns_queries_by_destination", now, map[string]float64{"cached": cached, "blocked": blocked, "forwarded": fwd})
	_ = reg.Collect("pihole.active_clients", now, map[string]float64{"active": clients})
	_ = reg.Collect("pihole.gravity_list_blocked_domains", now, map[string]float64{"blocked": domains})
	return nil
}

func (p *piholeCollector) summary(ctx context.Context) (map[string]any, error) {
	urls := []string{
		p.url + "/admin/api.php?summaryRaw",
		p.url + "/admin/api.php?summary",
		p.url + "/api/stats/summary",
	}
	if p.cfg.Token != "" {
		urls = []string{
			p.url + "/admin/api.php?summaryRaw&auth=" + p.cfg.Token,
			p.url + "/admin/api.php?summary&auth=" + p.cfg.Token,
		}
	}
	var last error
	for _, u := range urls {
		b, err := httpGet(ctx, p.client, u)
		if err != nil {
			last = err
			continue
		}
		m, err := jsonMap(b)
		if err != nil {
			last = err
			continue
		}
		if nestFloat(m, "dns_queries_today") > 0 || nestMap(m, "queries") != nil || nestFloat(m, "domains_being_blocked") > 0 {
			return m, nil
		}
		last = fmt.Errorf("unexpected response")
	}
	if last == nil {
		last = fmt.Errorf("no summary")
	}
	return nil, fmt.Errorf("pihole: %w", last)
}
