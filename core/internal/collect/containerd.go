package collect

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// containerdConfig is collectors.modules.containerd (`ctr containers/tasks ls`).
type containerdConfig struct {
	Command   string        `yaml:"command"`
	Namespace string        `yaml:"namespace"`
	Timeout   time.Duration `yaml:"timeout"`
}

type containerdCollector struct {
	cfg  containerdConfig
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	seen map[string]bool
}

func init() {
	Register("containerd", func() Collector { return &containerdCollector{} })
}

func (c *containerdCollector) Name() string { return "containerd" }

func (c *containerdCollector) Configure(decode func(v any) error) error {
	if err := decode(&c.cfg); err != nil {
		return err
	}
	if c.cfg.Command == "" {
		c.cfg.Command = "ctr"
	}
	if c.cfg.Namespace == "" {
		c.cfg.Namespace = "default"
	}
	if c.cfg.Timeout <= 0 {
		c.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (c *containerdCollector) Init(reg *registry.Registry) error {
	if c.cfg.Command == "" {
		if err := c.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if c.run == nil {
		c.run = execRun(c.cfg.Timeout)
	}
	if _, err := c.containers(context.Background()); err != nil {
		return err
	}
	c.seen = map[string]bool{}
	reg.AddChart(&registry.Chart{ID: "containerd.containers", Context: "containerd.containers", Title: "containerd containers by state",
		Units: "containers", Family: "containerd", Type: registry.Stacked, Priority: 30400, Plugin: "containerd", Module: "containerd",
		Dimensions: []*registry.Dimension{{ID: "running"}, {ID: "stopped"}, {ID: "other"}}})
	return nil
}

func (c *containerdCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	list, err := c.containers(ctx)
	if err != nil {
		return err
	}
	tasks := map[string]string{}
	if ts, err := c.tasks(ctx); err == nil {
		for _, t := range ts {
			tasks[t.Name] = t.State
		}
	}
	states := map[string]float64{"running": 0, "stopped": 0, "other": 0}
	for _, n := range list {
		st := strings.ToLower(tasks[n])
		if st == "" {
			st = "stopped"
		}
		switch {
		case strings.Contains(st, "run"):
			states["running"]++
			st = "running"
		case strings.Contains(st, "stop") || strings.Contains(st, "creat"):
			states["stopped"]++
			st = "stopped"
		default:
			states["other"]++
		}
		id := "containerd.container_state." + sanitizeID(n)
		if !c.seen[id] {
			c.seen[id] = true
			ch := sysChart(id, "containerd", "containerd "+n, "boolean", 30410, &registry.Dimension{ID: "running"})
			ch.Context, ch.Plugin, ch.Module = "containerd.container_state", "containerd", "containerd"
			reg.AddChart(ch)
		}
		run := 0.0
		if st == "running" {
			run = 1
		}
		_ = reg.Collect(id, now, map[string]float64{"running": run})
	}
	_ = reg.Collect("containerd.containers", now, states)
	return nil
}

func (c *containerdCollector) Functions() []Function {
	return []Function{{
		Name: "containerd-containers", Help: "containerd containers (id, state)", Timeout: 10,
		Run: func(ctx context.Context, args map[string]string) (any, error) {
			list, err := c.containers(ctx)
			if err != nil {
				return Table{}, err
			}
			tasks := map[string]string{}
			if ts, err := c.tasks(ctx); err == nil {
				for _, t := range ts {
					tasks[t.Name] = t.State
				}
			}
			out := Table{Columns: []string{"id", "state"}, Total: len(list)}
			out.Rows = make([]any, len(list))
			for i, n := range list {
				st := tasks[n]
				if st == "" {
					st = "stopped"
				}
				out.Rows[i] = map[string]string{"id": n, "state": st}
			}
			return out, nil
		},
	}}
}

type ctrTask struct {
	Name, State string
}

func parseCtrContainers(b []byte) []string {
	var out []string
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if i == 0 && (strings.EqualFold(fields[0], "CONTAINER") || strings.EqualFold(fields[0], "ID")) {
			continue
		}
		out = append(out, fields[0])
	}
	return out
}

func parseCtrTasks(b []byte) []ctrTask {
	var out []ctrTask
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if i == 0 && strings.EqualFold(fields[0], "TASK") {
			continue
		}
		st := fields[len(fields)-1]
		out = append(out, ctrTask{Name: fields[0], State: st})
	}
	return out
}

func (c *containerdCollector) nsArgs(rest ...string) []string {
	args := []string{"-n", c.cfg.Namespace}
	return append(args, rest...)
}

func (c *containerdCollector) containers(ctx context.Context) ([]string, error) {
	b, err := c.run(ctx, c.cfg.Command, c.nsArgs("containers", "ls")...)
	if err != nil {
		return nil, fmt.Errorf("containerd: %w", err)
	}
	list := parseCtrContainers(b)
	if len(list) == 0 {
		return nil, fmt.Errorf("containerd: no containers")
	}
	return list, nil
}

func (c *containerdCollector) tasks(ctx context.Context) ([]ctrTask, error) {
	b, err := c.run(ctx, c.cfg.Command, c.nsArgs("tasks", "ls")...)
	if err != nil {
		return nil, err
	}
	return parseCtrTasks(b), nil
}
