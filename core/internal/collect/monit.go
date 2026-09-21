package collect

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// monitConfig is collectors.modules.monit (HTTP _status XML).
type monitConfig struct {
	URL      string        `yaml:"url"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type monitCollector struct {
	cfg    monitConfig
	client *http.Client
	url    string
}

func init() {
	Register("monit", func() Collector { return &monitCollector{} })
}

func (m *monitCollector) Name() string { return "monit" }

func (m *monitCollector) Configure(decode func(v any) error) error {
	if err := decode(&m.cfg); err != nil {
		return err
	}
	if m.cfg.Timeout <= 0 {
		m.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (m *monitCollector) Init(reg *registry.Registry) error {
	if m.cfg.Timeout <= 0 {
		if err := m.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	m.client = &http.Client{Timeout: m.cfg.Timeout}
	urls := []string{m.cfg.URL}
	if m.cfg.URL == "" {
		urls = []string{"http://127.0.0.1:2812/_status?format=xml"}
	}
	var last error
	for _, cand := range urls {
		if cand == "" {
			continue
		}
		b, e := httpGetAuth(context.Background(), m.client, cand, m.cfg.User, m.cfg.Password)
		if e != nil {
			last = e
			continue
		}
		if !strings.Contains(string(b), "<monit") && !strings.Contains(string(b), "<service") {
			last = fmt.Errorf("monit: unexpected status XML")
			continue
		}
		if _, err := parseMonitStatus(b); err != nil {
			last = err
			continue
		}
		m.url = cand
		break
	}
	if m.url == "" {
		if last == nil {
			last = fmt.Errorf("monit: no status")
		}
		return last
	}
	for _, c := range []*registry.Chart{
		{ID: "monit.services", Title: "Monit Services", Units: "services", Type: registry.Stacked, Priority: 56000,
			Dimensions: []*registry.Dimension{{ID: "ok"}, {ID: "error"}, {ID: "unmonitored"}}},
	} {
		c.Family, c.Plugin, c.Module = "monit", "monit", "monit"
		reg.AddChart(c)
	}
	return nil
}

func (m *monitCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	b, err := httpGetAuth(ctx, m.client, m.url, m.cfg.User, m.cfg.Password)
	if err != nil {
		return err
	}
	svc, err := parseMonitStatus(b)
	if err != nil {
		return err
	}
	ok, bad, unmon := 0.0, 0.0, 0.0
	for _, s := range svc {
		switch {
		case !s.monitor:
			unmon++
		case s.status != 0:
			bad++
		default:
			ok++
		}
	}
	_ = reg.Collect("monit.services", now, map[string]float64{"ok": ok, "error": bad, "unmonitored": unmon})
	return nil
}

type monitSvc struct {
	name    string
	status  int
	monitor bool
}

func parseMonitStatus(b []byte) ([]monitSvc, error) {
	var raw struct {
		Services []struct {
			Name    string `xml:"name"`
			Status  int    `xml:"status"`
			Monitor int    `xml:"monitor"`
		} `xml:"service"`
	}
	if err := xml.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("monit: %w", err)
	}
	out := make([]monitSvc, 0, len(raw.Services))
	for _, s := range raw.Services {
		out = append(out, monitSvc{name: s.Name, status: s.Status, monitor: s.Monitor != 0})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("monit: no services")
	}
	return out, nil
}
