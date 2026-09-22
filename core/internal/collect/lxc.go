package collect

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// lxcConfig is collectors.modules.lxc (`lxc-ls -f` or cgroup dirs).
type lxcConfig struct {
	Command    string        `yaml:"command"`
	CgroupRoot string        `yaml:"cgroup_root"`
	Timeout    time.Duration `yaml:"timeout"`
}

type lxcCollector struct {
	cfg     lxcConfig
	run     func(ctx context.Context, name string, args ...string) ([]byte, error)
	list    func(ctx context.Context) ([]lxcCont, error)
	readDir func(dir string) ([]string, error)
	seen    map[string]bool
}

func init() {
	Register("lxc", func() Collector { return &lxcCollector{} })
}

func (l *lxcCollector) Name() string { return "lxc" }

func (l *lxcCollector) Configure(decode func(v any) error) error {
	if err := decode(&l.cfg); err != nil {
		return err
	}
	if l.cfg.Command == "" {
		l.cfg.Command = "lxc-ls"
	}
	if l.cfg.CgroupRoot == "" {
		l.cfg.CgroupRoot = "/sys/fs/cgroup"
	}
	if l.cfg.Timeout <= 0 {
		l.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (l *lxcCollector) Init(reg *registry.Registry) error {
	if l.cfg.Command == "" {
		if err := l.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if l.list == nil {
		l.list = l.discover
	}
	cs, err := l.list(context.Background())
	if err != nil {
		return err
	}
	if len(cs) == 0 {
		return fmt.Errorf("lxc: no containers")
	}
	l.seen = map[string]bool{}
	reg.AddChart(&registry.Chart{ID: "lxc.containers", Context: "lxc.containers", Title: "LXC containers by state",
		Units: "containers", Family: "lxc", Type: registry.Stacked, Priority: 30200, Plugin: "lxc", Module: "lxc",
		Dimensions: []*registry.Dimension{{ID: "running"}, {ID: "stopped"}, {ID: "other"}}})
	return nil
}

func (l *lxcCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	cs, err := l.list(ctx)
	if err != nil {
		return err
	}
	states := map[string]float64{"running": 0, "stopped": 0, "other": 0}
	for _, c := range cs {
		st := strings.ToLower(c.State)
		switch {
		case strings.Contains(st, "run"):
			states["running"]++
			st = "running"
		case strings.Contains(st, "stop") || st == "frozen":
			states["stopped"]++
			st = "stopped"
		default:
			states["other"]++
		}
		id := "lxc.container_state." + sanitizeID(c.Name)
		if !l.seen[id] {
			l.seen[id] = true
			ch := sysChart(id, "lxc", "LXC container "+c.Name, "boolean", 30210, &registry.Dimension{ID: "running"})
			ch.Context, ch.Plugin, ch.Module = "lxc.container_state", "lxc", "lxc"
			reg.AddChart(ch)
		}
		run := 0.0
		if st == "running" {
			run = 1
		}
		_ = reg.Collect(id, now, map[string]float64{"running": run})
	}
	_ = reg.Collect("lxc.containers", now, states)
	return nil
}

func (l *lxcCollector) Functions() []Function {
	return []Function{{
		Name: "lxc-containers", Help: "LXC containers (name, state)", Timeout: 10,
		Run: func(ctx context.Context, args map[string]string) (any, error) {
			cs, err := l.list(ctx)
			if err != nil {
				return Table{}, err
			}
			out := Table{Columns: []string{"name", "state"}, Total: len(cs)}
			out.Rows = make([]any, len(cs))
			for i, c := range cs {
				out.Rows[i] = map[string]string{"name": c.Name, "state": c.State}
			}
			return out, nil
		},
	}}
}

type lxcCont struct {
	Name, State string
}

func parseLxcLs(b []byte) []lxcCont {
	var out []lxcCont
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if i == 0 && strings.EqualFold(fields[0], "NAME") {
			continue
		}
		if strings.HasPrefix(fields[0], "-") {
			continue
		}
		out = append(out, lxcCont{Name: fields[0], State: fields[1]})
	}
	return out
}

func (l *lxcCollector) discover(ctx context.Context) ([]lxcCont, error) {
	if l.run == nil {
		l.run = execRun(l.cfg.Timeout)
	}
	if b, err := l.run(ctx, l.cfg.Command, "-f"); err == nil {
		if cs := parseLxcLs(b); len(cs) > 0 {
			return cs, nil
		}
	}
	return l.fromCgroup()
}

func (l *lxcCollector) fromCgroup() ([]lxcCont, error) {
	readDir := l.readDir
	if readDir == nil {
		readDir = func(dir string) ([]string, error) {
			ents, err := os.ReadDir(dir)
			if err != nil {
				return nil, err
			}
			out := make([]string, 0, len(ents))
			for _, e := range ents {
				if e.IsDir() {
					out = append(out, e.Name())
				}
			}
			return out, nil
		}
	}
	var cs []lxcCont
	for _, sub := range []string{"lxc", "lxc.payload", "lxc.monitor"} {
		names, err := readDir(filepath.Join(l.cfg.CgroupRoot, sub))
		if err != nil {
			continue
		}
		for _, n := range names {
			cs = append(cs, lxcCont{Name: n, State: "running"})
		}
	}
	if len(cs) == 0 {
		return nil, fmt.Errorf("lxc: no containers")
	}
	return cs, nil
}
