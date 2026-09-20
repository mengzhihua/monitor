package collect

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// nginxConfig is the `collectors.modules.nginx` section (ngx_http_stub_status_module).
type nginxConfig struct {
	URL     string        `yaml:"url"` // default http://127.0.0.1/stub_status
	Timeout time.Duration `yaml:"timeout"`
}

type nginxCollector struct {
	cfg    nginxConfig
	client *http.Client
}

func init() {
	Register("nginx", func() Collector { return &nginxCollector{} })
}

func (n *nginxCollector) Name() string { return "nginx" }

func (n *nginxCollector) Configure(decode func(v any) error) error {
	if err := decode(&n.cfg); err != nil {
		return err
	}
	if n.cfg.URL == "" {
		n.cfg.URL = "http://127.0.0.1/stub_status"
	}
	if n.cfg.Timeout <= 0 {
		n.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (n *nginxCollector) Init(reg *registry.Registry) error {
	if n.cfg.URL == "" {
		if err := n.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	n.client = &http.Client{Timeout: n.cfg.Timeout}
	if _, err := n.fetch(context.Background()); err != nil {
		return err
	}
	reg.AddChart(&registry.Chart{ID: "nginx.connections", Family: "nginx", Title: "nginx active connections", Units: "connections",
		Priority: 40000, Plugin: "nginx", Module: "nginx",
		Dimensions: []*registry.Dimension{{ID: "active"}}})
	reg.AddChart(&registry.Chart{ID: "nginx.connections_status", Family: "nginx", Title: "nginx connections by status", Units: "connections",
		Type: registry.Stacked, Priority: 40010, Plugin: "nginx", Module: "nginx",
		Dimensions: []*registry.Dimension{{ID: "reading"}, {ID: "writing"}, {ID: "waiting", Name: "idle"}}})
	reg.AddChart(&registry.Chart{ID: "nginx.connections_accepted_handled", Family: "nginx", Title: "nginx connections accepted/handled", Units: "connections/s",
		Priority: 40020, Plugin: "nginx", Module: "nginx",
		Dimensions: []*registry.Dimension{{ID: "accepted", Algorithm: registry.Incremental}, {ID: "handled", Algorithm: registry.Incremental}}})
	reg.AddChart(&registry.Chart{ID: "nginx.requests", Family: "nginx", Title: "nginx requests", Units: "requests/s",
		Priority: 40030, Plugin: "nginx", Module: "nginx",
		Dimensions: []*registry.Dimension{{ID: "requests", Algorithm: registry.Incremental}}})
	return nil
}

type nginxStatus struct {
	active, reading, writing, waiting, accepted, handled, requests float64
}

// parseStubStatus parses the classic three-block stub_status text:
//
//	Active connections: 291
//	server accepts handled requests
//	 16630948 16630948 31070465
//	Reading: 6 Writing: 179 Waiting: 106
func parseStubStatus(s string) (nginxStatus, error) {
	var st nginxStatus
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) < 4 {
		return st, fmt.Errorf("nginx: unexpected stub_status body %q", s)
	}
	f := func(v string) float64 { n, _ := strconv.ParseFloat(strings.TrimSpace(v), 64); return n }
	if _, v, ok := strings.Cut(lines[0], ":"); ok {
		st.active = f(v)
	}
	counters := strings.Fields(lines[2])
	if len(counters) != 3 {
		return st, fmt.Errorf("nginx: unexpected counters line %q", lines[2])
	}
	st.accepted, st.handled, st.requests = f(counters[0]), f(counters[1]), f(counters[2])
	fields := strings.Fields(lines[3])
	for i := 0; i+1 < len(fields); i += 2 {
		switch strings.TrimSuffix(fields[i], ":") {
		case "Reading":
			st.reading = f(fields[i+1])
		case "Writing":
			st.writing = f(fields[i+1])
		case "Waiting":
			st.waiting = f(fields[i+1])
		}
	}
	return st, nil
}

func (n *nginxCollector) fetch(ctx context.Context) (nginxStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.cfg.URL, nil)
	if err != nil {
		return nginxStatus{}, err
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return nginxStatus{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nginxStatus{}, fmt.Errorf("nginx: %s -> %s", n.cfg.URL, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nginxStatus{}, err
	}
	return parseStubStatus(string(body))
}

func (n *nginxCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	st, err := n.fetch(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("nginx.connections", now, map[string]float64{"active": st.active})
	_ = reg.Collect("nginx.connections_status", now, map[string]float64{"reading": st.reading, "writing": st.writing, "waiting": st.waiting})
	_ = reg.Collect("nginx.connections_accepted_handled", now, map[string]float64{"accepted": st.accepted, "handled": st.handled})
	_ = reg.Collect("nginx.requests", now, map[string]float64{"requests": st.requests})
	return nil
}
