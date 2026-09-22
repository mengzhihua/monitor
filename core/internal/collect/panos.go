package collect

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// panosConfig is collectors.modules.panos (PAN-OS XML API).
type panosConfig struct {
	TLS      CollectorTLS  `yaml:"tls"`
	URL      string        `yaml:"url"`
	APIKey   string        `yaml:"api_key"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type panosCollector struct {
	cfg    panosConfig
	client *http.Client
	url    string
	key    string
}

func init() {
	Register("panos", func() Collector { return &panosCollector{} })
}

func (p *panosCollector) Name() string { return "panos" }

func (p *panosCollector) Configure(decode func(v any) error) error {
	if err := decode(&p.cfg); err != nil {
		return err
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (p *panosCollector) Init(reg *registry.Registry) error {
	if p.cfg.Timeout <= 0 {
		if err := p.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	client, tlsErr := collectorHTTPClient(p.cfg.Timeout, p.cfg.TLS)
	if tlsErr != nil {
		return tlsErr
	}
	p.client = client
	base := strings.TrimRight(p.cfg.URL, "/")
	if base == "" {
		base = "https://127.0.0.1"
	}
	p.url = strings.TrimSuffix(base, "/api")
	p.key = p.cfg.APIKey
	if p.key == "" && p.cfg.User != "" {
		k, err := p.genKey(context.Background())
		if err != nil {
			return err
		}
		p.key = k
	}
	if _, err := p.sessions(context.Background()); err != nil {
		return err
	}
	for _, ch := range []*registry.Chart{
		{ID: "panos.sessions", Title: "Active sessions", Units: "sessions", Type: registry.Stacked, Priority: 61400,
			Dimensions: []*registry.Dimension{{ID: "active"}, {ID: "tcp"}, {ID: "udp"}}},
		{ID: "panos.session_utilization", Title: "Session table utilization", Units: "%", Priority: 61410,
			Dimensions: []*registry.Dimension{{ID: "utilization"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "panos", "panos", "panos"
		reg.AddChart(ch)
	}
	return nil
}

func (p *panosCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	st, err := p.sessions(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("panos.sessions", now, map[string]float64{"active": st["active"], "tcp": st["tcp"], "udp": st["udp"]})
	_ = reg.Collect("panos.session_utilization", now, map[string]float64{"utilization": st["utilization"]})
	return nil
}

func (p *panosCollector) genKey(ctx context.Context) (string, error) {
	u := p.url + "/api/?type=keygen&user=" + url.QueryEscape(p.cfg.User) + "&password=" + url.QueryEscape(p.cfg.Password)
	b, err := httpGet(ctx, p.client, u)
	if err != nil {
		return "", fmt.Errorf("panos: %w", err)
	}
	k := xmlTag(string(b), "key")
	if k == "" {
		return "", fmt.Errorf("panos: no api key")
	}
	return k, nil
}

func (p *panosCollector) sessions(ctx context.Context) (map[string]float64, error) {
	cmd := "<show><session><info></info></session></show>"
	u := p.url + "/api/?type=op&cmd=" + url.QueryEscape(cmd)
	if p.key != "" {
		u += "&key=" + url.QueryEscape(p.key)
	}
	b, err := httpGet(ctx, p.client, u)
	if err != nil {
		return nil, fmt.Errorf("panos: %w", err)
	}
	s := string(b)
	if !strings.Contains(s, "num-active") && !strings.Contains(s, "num-tcp") && xmlTag(s, "status") == "error" {
		return nil, fmt.Errorf("panos: error response")
	}
	out := map[string]float64{
		"active":      firstFloat(xmlTag(s, "num-active")),
		"tcp":         firstFloat(xmlTag(s, "num-tcp")),
		"udp":         firstFloat(xmlTag(s, "num-udp")),
		"utilization": firstFloat(xmlTag(s, "session-utilization")),
	}
	if out["active"] == 0 && out["tcp"] == 0 && out["udp"] == 0 && !strings.Contains(s, "num-active") {
		return nil, fmt.Errorf("panos: no session info")
	}
	return out, nil
}
