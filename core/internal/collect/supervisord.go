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

// supervisordConfig is collectors.modules.supervisord (XML-RPC).
type supervisordConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type supervisordCollector struct {
	cfg    supervisordConfig
	client *http.Client
	url    string
	seen   map[string]bool
}

func init() {
	Register("supervisord", func() Collector { return &supervisordCollector{} })
}

func (s *supervisordCollector) Name() string { return "supervisord" }

func (s *supervisordCollector) Configure(decode func(v any) error) error {
	if err := decode(&s.cfg); err != nil {
		return err
	}
	if s.cfg.Timeout <= 0 {
		s.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (s *supervisordCollector) Init(reg *registry.Registry) error {
	if s.cfg.Timeout <= 0 {
		if err := s.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	s.client = &http.Client{Timeout: s.cfg.Timeout}
	s.seen = map[string]bool{}
	urls := []string{s.cfg.URL}
	if s.cfg.URL == "" {
		urls = []string{"http://127.0.0.1:9001/RPC2", "http://127.0.0.1:9001/RPC2/"}
	}
	var last error
	for _, u := range urls {
		if u == "" {
			continue
		}
		if _, err := s.procs(context.Background(), u); err != nil {
			last = err
			continue
		}
		s.url = u
		break
	}
	if s.url == "" {
		if last == nil {
			last = fmt.Errorf("supervisord: no XML-RPC")
		}
		return last
	}
	for _, c := range []*registry.Chart{
		{ID: "supervisord.summary_processes", Title: "Processes", Units: "processes", Type: registry.Stacked, Priority: 55900,
			Dimensions: []*registry.Dimension{{ID: "running"}, {ID: "non-running"}}},
	} {
		c.Family, c.Plugin, c.Module = "supervisord", "supervisord", "supervisord"
		reg.AddChart(c)
	}
	return nil
}

func (s *supervisordCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	procs, err := s.procs(ctx, s.url)
	if err != nil {
		return err
	}
	running, other := 0.0, 0.0
	byGroup := map[string][2]float64{}
	for _, p := range procs {
		if strings.EqualFold(p.State, "RUNNING") {
			running++
			g := byGroup[p.Group]
			g[0]++
			byGroup[p.Group] = g
		} else {
			other++
			g := byGroup[p.Group]
			g[1]++
			byGroup[p.Group] = g
		}
	}
	_ = reg.Collect("supervisord.summary_processes", now, map[string]float64{"running": running, "non-running": other})
	for g, n := range byGroup {
		s.ensureGroup(reg, g)
		id := sanitizeID(g)
		_ = reg.Collect("supervisord.processes."+id, now, map[string]float64{"running": n[0], "non-running": n[1]})
	}
	return nil
}

func (s *supervisordCollector) ensureGroup(reg *registry.Registry, group string) {
	if s.seen[group] {
		return
	}
	s.seen[group] = true
	id := sanitizeID(group)
	c := &registry.Chart{ID: "supervisord.processes." + id, Context: "supervisord.processes", Title: "Processes", Units: "processes",
		Type: registry.Stacked, Priority: 55910, Labels: map[string]string{"group": group},
		Dimensions: []*registry.Dimension{{ID: "running"}, {ID: "non-running"}}}
	c.Family, c.Plugin, c.Module = "supervisord", "supervisord", "supervisord"
	reg.AddChart(c)
}

type superProc struct {
	Name, Group, State string
}

func (s *supervisordCollector) procs(ctx context.Context, rpcURL string) ([]superProc, error) {
	body := `<?xml version="1.0"?><methodCall><methodName>supervisor.getAllProcessInfo</methodName><params></params></methodCall>`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/xml")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("supervisord: %s", resp.Status)
	}
	var parsed xmlRPC
	if err := xml.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("supervisord: %w", err)
	}
	out := parsed.procs()
	if len(out) == 0 {
		return nil, fmt.Errorf("supervisord: no processes")
	}
	return out, nil
}

type xmlRPC struct {
	Params struct {
		Param struct {
			Value struct {
				Array struct {
					Data struct {
						Values []xmlRPCValue `xml:"value"`
					} `xml:"data"`
				} `xml:"array"`
			} `xml:"value"`
		} `xml:"param"`
	} `xml:"params"`
}

type xmlRPCValue struct {
	Struct struct {
		Members []struct {
			Name  string `xml:"name"`
			Value struct {
				String string `xml:"string"`
				Int    string `xml:"int"`
			} `xml:"value"`
		} `xml:"member"`
	} `xml:"struct"`
}

func (x xmlRPC) procs() []superProc {
	var out []superProc
	for _, v := range x.Params.Param.Value.Array.Data.Values {
		p := superProc{}
		for _, m := range v.Struct.Members {
			val := m.Value.String
			if val == "" {
				val = m.Value.Int
			}
			switch m.Name {
			case "name":
				p.Name = val
			case "group":
				p.Group = val
			case "statename":
				p.State = val
			}
		}
		if p.Group == "" {
			p.Group = p.Name
		}
		if p.Name != "" {
			out = append(out, p)
		}
	}
	return out
}
