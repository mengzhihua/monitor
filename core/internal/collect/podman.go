package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// podmanConfig is collectors.modules.podman (Docker-compatible Podman REST).
type podmanConfig struct {
	Address string        `yaml:"address"`
	Timeout time.Duration `yaml:"timeout"`
}

type podmanCollector struct {
	cfg    podmanConfig
	client *http.Client
	base   string
	get    func(ctx context.Context, path string, out any) error
	seen   map[string]bool
}

func init() {
	Register("podman", func() Collector { return &podmanCollector{} })
}

func (p *podmanCollector) Name() string { return "podman" }

func (p *podmanCollector) Configure(decode func(v any) error) error {
	if err := decode(&p.cfg); err != nil {
		return err
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 5 * time.Second
	}
	if p.cfg.Address == "" {
		if env := os.Getenv("CONTAINER_HOST"); env != "" {
			p.cfg.Address = env
		} else if runtime.GOOS == "windows" {
			p.cfg.Address = "npipe:////./pipe/podman-machine-default"
		} else {
			p.cfg.Address = "unix:///run/podman/podman.sock"
		}
	}
	return nil
}

func (p *podmanCollector) Init(reg *registry.Registry) error {
	if p.cfg.Timeout == 0 {
		if err := p.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if p.get == nil {
		if err := p.dial(); err != nil {
			return err
		}
		p.get = p.httpGet
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.cfg.Timeout)
	defer cancel()
	var ping struct {
		Version string `json:"Version"`
	}
	if err := p.get(ctx, "/v4.0.0/libpod/info", &ping); err != nil {
		var ver struct {
			Version string `json:"Version"`
		}
		if err2 := p.get(ctx, "/version", &ver); err2 != nil {
			return fmt.Errorf("podman not reachable at %s: %w", p.cfg.Address, err)
		}
	}
	p.seen = map[string]bool{}
	reg.AddChart(&registry.Chart{ID: "podman.containers", Family: "podman", Title: "Podman containers by state",
		Units: "containers", Type: registry.Stacked, Priority: 30100, Plugin: "podman", Module: "podman",
		Dimensions: []*registry.Dimension{{ID: "running"}, {ID: "paused"}, {ID: "exited"}, {ID: "other"}}})
	return nil
}

func (p *podmanCollector) dial() error {
	u, err := url.Parse(p.cfg.Address)
	if err != nil {
		return fmt.Errorf("podman address: %w", err)
	}
	tr := &http.Transport{MaxIdleConns: 4, IdleConnTimeout: 90 * time.Second}
	switch u.Scheme {
	case "unix":
		path := u.Path
		if path == "" {
			path = u.Host
		}
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}
		p.base = "http://podman"
	case "tcp", "http":
		p.base = "http://" + u.Host
	case "https":
		p.base = "https://" + u.Host
	default:
		return fmt.Errorf("podman address: unsupported scheme %q", u.Scheme)
	}
	p.client = &http.Client{Transport: tr, Timeout: p.cfg.Timeout}
	return nil
}

func (p *podmanCollector) httpGet(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("podman: %s -> %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (p *podmanCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	var list []dockerListEntry
	if err := p.get(ctx, "/containers/json?all=1", &list); err != nil {
		return err
	}
	states := map[string]float64{"running": 0, "paused": 0, "exited": 0, "other": 0}
	names := make([]string, 0, len(list))
	for _, e := range list {
		name := dockerContName(e)
		names = append(names, name)
		switch strings.ToLower(e.State) {
		case "running":
			states["running"]++
		case "paused":
			states["paused"]++
		case "exited", "stopped":
			states["exited"]++
		default:
			states["other"]++
		}
		id := "podman.container_state." + sanitizeID(name)
		if !p.seen[id] {
			p.seen[id] = true
			ch := sysChart(id, "podman", "Podman container "+name, "boolean", 30110, &registry.Dimension{ID: "running"})
			ch.Plugin, ch.Module = "podman", "podman"
			reg.AddChart(ch)
		}
		run := 0.0
		if strings.EqualFold(e.State, "running") {
			run = 1
		}
		_ = reg.Collect(id, now, map[string]float64{"running": run})
	}
	_ = names
	_ = reg.Collect("podman.containers", now, states)
	sort.Strings(names)
	return nil
}

func (p *podmanCollector) Functions() []Function {
	return []Function{{
		Name: "podman-containers", Help: "Podman containers (name, image, state)", Timeout: 10,
		Run: func(ctx context.Context, args map[string]string) (any, error) {
			if p.get == nil {
				return Table{}, fmt.Errorf("podman not connected")
			}
			var list []dockerListEntry
			if err := p.get(ctx, "/containers/json?all=1", &list); err != nil {
				return Table{}, err
			}
			rows := make([]ContainerRow, 0, len(list))
			for _, e := range list {
				id := e.ID
				if len(id) > 12 {
					id = id[:12]
				}
				rows = append(rows, ContainerRow{ID: id, Name: dockerContName(e), Image: e.Image, State: e.State, Status: e.Status})
			}
			out := Table{Columns: []string{"id", "name", "image", "state", "status"}, Total: len(rows)}
			out.Rows = make([]any, len(rows))
			for i, r := range rows {
				out.Rows[i] = r
			}
			return out, nil
		},
	}}
}
