package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

const catoSitesQuery = `query { accountSnapshot { sites { id name hosts connectivityStatus operationalStatus bytesUpstreamMax bytesDownstreamMax lostUpstreamPercent lostDownstreamPercent rttMs } } }`

// catoNetworksConfig is collectors.modules.cato_networks (Cato GraphQL API).
type catoNetworksConfig struct {
	URL       string        `yaml:"url"`
	APIKey    string        `yaml:"api_key"`
	AccountID string        `yaml:"account_id"`
	Timeout   time.Duration `yaml:"timeout"`
}

type catoNetworksCollector struct {
	cfg    catoNetworksConfig
	client *http.Client
	url    string
	seen   map[string]bool
}

func init() {
	Register("cato_networks", func() Collector { return &catoNetworksCollector{} })
}

func (c *catoNetworksCollector) Name() string { return "cato_networks" }

func (c *catoNetworksCollector) Configure(decode func(v any) error) error {
	if err := decode(&c.cfg); err != nil {
		return err
	}
	if c.cfg.URL == "" {
		c.cfg.URL = "https://api.catonetworks.com/api/v1/graphql2"
	}
	if c.cfg.Timeout <= 0 {
		c.cfg.Timeout = 8 * time.Second
	}
	return nil
}

func (c *catoNetworksCollector) Init(reg *registry.Registry) error {
	if c.cfg.Timeout <= 0 {
		if err := c.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if strings.TrimSpace(c.cfg.APIKey) == "" {
		return fmt.Errorf("cato_networks: no api_key")
	}
	c.client = &http.Client{Timeout: c.cfg.Timeout}
	c.url = c.cfg.URL
	c.seen = map[string]bool{}
	if _, err := c.sites(context.Background()); err != nil {
		return err
	}
	return nil
}

func (c *catoNetworksCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	sites, err := c.sites(ctx)
	if err != nil {
		return err
	}
	for _, s := range sites {
		c.ensure(reg, s)
		id := sanitizeID(s.id)
		conn := 0.0
		if s.connected {
			conn = 1
		}
		op := 0.0
		if s.operational {
			op = 1
		}
		_ = reg.Collect("cato_networks.site_hosts."+id, now, map[string]float64{"hosts": s.hosts})
		_ = reg.Collect("cato_networks.site_connectivity_status."+id, now, map[string]float64{"status": conn})
		_ = reg.Collect("cato_networks.site_operational_status."+id, now, map[string]float64{"status": op})
		_ = reg.Collect("cato_networks.site_traffic."+id, now, map[string]float64{"upstream": s.up, "downstream": s.down})
		_ = reg.Collect("cato_networks.site_packet_loss."+id, now, map[string]float64{"upstream": s.lossUp, "downstream": s.lossDown})
		_ = reg.Collect("cato_networks.site_latency."+id, now, map[string]float64{"rtt": s.rtt})
	}
	return nil
}

type catoSite struct {
	id, name                               string
	hosts, up, down, lossUp, lossDown, rtt float64
	connected, operational                 bool
}

func (c *catoNetworksCollector) ensure(reg *registry.Registry, s catoSite) {
	if c.seen[s.id] {
		return
	}
	c.seen[s.id] = true
	id := sanitizeID(s.id)
	labels := map[string]string{"site_id": s.id, "site_name": s.name}
	charts := []*registry.Chart{
		{ID: "cato_networks.site_hosts." + id, Context: "cato_networks.site_hosts", Title: "Cato site hosts", Units: "hosts", Family: "sites", Priority: 62700, Labels: labels, Dimensions: []*registry.Dimension{{ID: "hosts"}}},
		{ID: "cato_networks.site_connectivity_status." + id, Context: "cato_networks.site_connectivity_status", Title: "Cato site connectivity status", Units: "status", Family: "sites", Priority: 62710, Labels: labels, Dimensions: []*registry.Dimension{{ID: "status"}}},
		{ID: "cato_networks.site_operational_status." + id, Context: "cato_networks.site_operational_status", Title: "Cato site operational status", Units: "status", Family: "sites", Priority: 62720, Labels: labels, Dimensions: []*registry.Dimension{{ID: "status"}}},
		{ID: "cato_networks.site_traffic." + id, Context: "cato_networks.site_traffic", Title: "Cato site traffic", Units: "bytes", Family: "sites", Type: registry.Area, Priority: 62730, Labels: labels, Dimensions: []*registry.Dimension{{ID: "upstream"}, {ID: "downstream"}}},
		{ID: "cato_networks.site_packet_loss." + id, Context: "cato_networks.site_packet_loss", Title: "Cato site packet loss", Units: "percentage", Family: "sites", Priority: 62740, Labels: labels, Dimensions: []*registry.Dimension{{ID: "upstream"}, {ID: "downstream"}}},
		{ID: "cato_networks.site_latency." + id, Context: "cato_networks.site_latency", Title: "Cato site latency", Units: "milliseconds", Family: "sites", Priority: 62750, Labels: labels, Dimensions: []*registry.Dimension{{ID: "rtt"}}},
	}
	for _, ch := range charts {
		ch.Plugin, ch.Module = "cato_networks", "cato_networks"
		reg.AddChart(ch)
	}
}

func (c *catoNetworksCollector) sites(ctx context.Context) ([]catoSite, error) {
	payload, _ := json.Marshal(map[string]any{
		"query": catoSitesQuery,
		"variables": map[string]any{
			"accountID": c.cfg.AccountID,
		},
	})
	hdr := map[string]string{"x-api-key": c.cfg.APIKey, "x-account-id": c.cfg.AccountID}
	b, err := httpPostBody(ctx, c.client, c.url, "application/json", string(payload), hdr)
	if err != nil {
		return nil, fmt.Errorf("cato_networks: %w", err)
	}
	m, err := jsonMap(b)
	if err != nil {
		return nil, fmt.Errorf("cato_networks: %w", err)
	}
	if errs := nestSlice(m, "errors"); len(errs) > 0 {
		return nil, fmt.Errorf("cato_networks: graphql error")
	}
	items := nestSlice(m, "data", "accountSnapshot", "sites")
	if len(items) == 0 {
		items = nestSlice(m, "data", "sites")
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("cato_networks: no sites")
	}
	var out []catoSite
	for _, it := range items {
		id := nestString(it, "id")
		if id == "" {
			id = nestString(it, "name")
		}
		if id == "" {
			continue
		}
		conn := strings.ToLower(nestString(it, "connectivityStatus"))
		op := strings.ToLower(nestString(it, "operationalStatus"))
		out = append(out, catoSite{
			id: id, name: nestString(it, "name"),
			hosts:       nestFloat(it, "hosts"),
			up:          nestFloat(it, "bytesUpstreamMax"),
			down:        nestFloat(it, "bytesDownstreamMax"),
			lossUp:      nestFloat(it, "lostUpstreamPercent"),
			lossDown:    nestFloat(it, "lostDownstreamPercent"),
			rtt:         nestFloat(it, "rttMs"),
			connected:   conn == "connected" || conn == "up" || conn == "1" || conn == "",
			operational: op == "active" || op == "up" || op == "1" || op == "",
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("cato_networks: no sites")
	}
	return out, nil
}
