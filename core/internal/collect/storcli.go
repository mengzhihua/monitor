package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// storcliConfig is collectors.modules.storcli (`storcli show all J`).
type storcliConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type storcliCollector struct {
	cfg  storcliConfig
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	seen map[string]bool
}

func init() {
	Register("storcli", func() Collector { return &storcliCollector{} })
}

func (s *storcliCollector) Name() string { return "storcli" }

func (s *storcliCollector) Configure(decode func(v any) error) error {
	if err := decode(&s.cfg); err != nil {
		return err
	}
	if s.cfg.Command == "" {
		s.cfg.Command = "storcli"
	}
	if s.cfg.Timeout <= 0 {
		s.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (s *storcliCollector) Init(reg *registry.Registry) error {
	if s.cfg.Command == "" {
		if err := s.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if s.run == nil {
		s.run = execRun(s.cfg.Timeout)
	}
	s.seen = map[string]bool{}
	ctrls, err := s.controllers(context.Background())
	if err != nil {
		return err
	}
	if len(ctrls) == 0 {
		return fmt.Errorf("storcli: no controllers")
	}
	return nil
}

func (s *storcliCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	ctrls, err := s.controllers(ctx)
	if err != nil {
		return err
	}
	for _, c := range ctrls {
		id := sanitizeID(c.id)
		if !s.seen[id] {
			s.seen[id] = true
			for _, ch := range []*registry.Chart{
				{ID: "storcli.controller_health_status." + id, Context: "storcli.controller_health_status", Title: "Controller health status", Units: "status", Priority: 58400,
					Dimensions: []*registry.Dimension{{ID: "healthy"}, {ID: "unhealthy"}}},
				{ID: "storcli.controller_status." + id, Context: "storcli.controller_status", Title: "Controller status", Units: "status", Priority: 58410,
					Dimensions: []*registry.Dimension{{ID: "optimal"}, {ID: "degraded"}, {ID: "partially_degraded"}, {ID: "failed"}}},
			} {
				ch.Family, ch.Plugin, ch.Module = "storcli", "storcli", "storcli"
				reg.AddChart(ch)
			}
		}
		st := strings.ToLower(c.status)
		healthy := bool01(st == "optimal" || st == "ok" || st == "success")
		_ = reg.Collect("storcli.controller_health_status."+id, now, map[string]float64{"healthy": healthy, "unhealthy": 1 - healthy})
		_ = reg.Collect("storcli.controller_status."+id, now, map[string]float64{
			"optimal": bool01(st == "optimal" || st == "ok"), "degraded": bool01(st == "degraded"),
			"partially_degraded": bool01(strings.Contains(st, "partial")), "failed": bool01(st == "failed"),
		})
	}
	return nil
}

type storcliCtrl struct{ id, status string }

func (s *storcliCollector) controllers(ctx context.Context) ([]storcliCtrl, error) {
	try := [][]string{{"show", "all", "J"}, {"/cALL", "show", "all", "J"}, {"show", "J"}}
	var last error
	for _, args := range try {
		b, err := s.run(ctx, s.cfg.Command, args...)
		if err != nil {
			last = err
			continue
		}
		if ctrls := parseStorcli(b); len(ctrls) > 0 {
			return ctrls, nil
		}
		last = fmt.Errorf("storcli: no controllers")
	}
	if last == nil {
		last = fmt.Errorf("storcli: no output")
	}
	return nil, last
}

func parseStorcli(b []byte) []storcliCtrl {
	s := strings.TrimSpace(string(b))
	if strings.HasPrefix(s, "{") {
		return parseStorcliJSON(b)
	}
	return parseStorcliText(s)
}

func parseStorcliJSON(b []byte) []storcliCtrl {
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		return nil
	}
	var out []storcliCtrl
	for _, c := range nestSlice(root, "Controllers") {
		m, _ := c.(map[string]any)
		id := fmt.Sprintf("%.0f", nestFloat(m, "Command Status", "Controller"))
		if id == "0" && nestString(m, "Command Status", "Controller") == "" {
			id = fmt.Sprintf("%.0f", nestFloat(m, "Response Data", "Basics", "Controller"))
		}
		st := nestString(m, "Response Data", "Status", "Controller Status")
		if st == "" {
			st = nestString(m, "Command Status", "Status")
		}
		if st == "" {
			st = "Optimal"
		}
		out = append(out, storcliCtrl{id: id, status: st})
	}
	return out
}

func parseStorcliText(s string) []storcliCtrl {
	var out []storcliCtrl
	id, status := "0", ""
	flush := func() {
		if status != "" {
			out = append(out, storcliCtrl{id: id, status: status})
			status = ""
		}
	}
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		low := strings.ToLower(t)
		switch {
		case strings.HasPrefix(low, "controller =") || strings.HasPrefix(low, "controller="):
			flush()
			id = colonVal(strings.ReplaceAll(t, "=", ":"))
		case strings.Contains(low, "controller status"):
			status = colonVal(strings.ReplaceAll(t, "=", ":"))
		}
	}
	flush()
	return out
}
