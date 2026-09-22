package collect

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// cupsConfig is collectors.modules.cups (Netdata cups.plugin via lpstat).
type cupsConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type cupsCollector struct {
	cfg  cupsConfig
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	seen map[string]bool
}

func init() {
	Register("cups", func() Collector { return &cupsCollector{} })
}

func (c *cupsCollector) Name() string { return "cups" }

func (c *cupsCollector) Configure(decode func(v any) error) error {
	if err := decode(&c.cfg); err != nil {
		return err
	}
	if c.cfg.Command == "" {
		c.cfg.Command = "lpstat"
	}
	if c.cfg.Timeout <= 0 {
		c.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (c *cupsCollector) Init(reg *registry.Registry) error {
	if c.cfg.Command == "" {
		if err := c.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if c.run == nil {
		c.run = execRun(c.cfg.Timeout)
	}
	if _, err := c.run(context.Background(), c.cfg.Command, "-p"); err != nil {
		return fmt.Errorf("cups: lpstat unavailable: %w", err)
	}
	c.seen = map[string]bool{}
	ch := sysChart("cups.dests", "cups", "CUPS destinations", "dests", 36000,
		&registry.Dimension{ID: "idle"}, &registry.Dimension{ID: "printing"}, &registry.Dimension{ID: "stopped"})
	ch.Plugin, ch.Module, ch.Family = "cups", "cups", "cups"
	reg.AddChart(ch)
	jobs := sysChart("cups.jobs", "cups", "CUPS jobs", "jobs", 36001, &registry.Dimension{ID: "pending"})
	jobs.Plugin, jobs.Module, jobs.Family = "cups", "cups", "cups"
	reg.AddChart(jobs)
	return nil
}

func (c *cupsCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	raw, err := c.run(ctx, c.cfg.Command, "-p")
	if err != nil {
		return err
	}
	dests := parseLpstatPrinters(string(raw))
	idle, printing, stopped := 0.0, 0.0, 0.0
	for _, d := range dests {
		switch d.State {
		case "printing":
			printing++
		case "stopped", "disabled":
			stopped++
		default:
			idle++
		}
		id := "cups.dest_state." + sanitizeID(d.Name)
		if !c.seen[id] {
			c.seen[id] = true
			ch := sysChart(id, "cups", "CUPS destination "+d.Name, "state", 36010,
				&registry.Dimension{ID: "idle"}, &registry.Dimension{ID: "printing"}, &registry.Dimension{ID: "stopped"})
			ch.Plugin, ch.Module = "cups", "cups"
			reg.AddChart(ch)
		}
		vals := map[string]float64{"idle": 0, "printing": 0, "stopped": 0}
		switch d.State {
		case "printing":
			vals["printing"] = 1
		case "stopped", "disabled":
			vals["stopped"] = 1
		default:
			vals["idle"] = 1
		}
		_ = reg.Collect(id, now, vals)
	}
	_ = reg.Collect("cups.dests", now, map[string]float64{"idle": idle, "printing": printing, "stopped": stopped})
	jobRaw, err := c.run(ctx, c.cfg.Command, "-o")
	n := 0.0
	if err == nil {
		n = float64(parseLpstatJobs(string(jobRaw)))
	}
	_ = reg.Collect("cups.jobs", now, map[string]float64{"pending": n})
	return nil
}

type cupsDest struct{ Name, State string }

func parseLpstatPrinters(s string) []cupsDest {
	var out []cupsDest
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || f[0] != "printer" {
			continue
		}
		st := "idle"
		line := strings.ToLower(sc.Text())
		switch {
		case strings.Contains(line, "printing"):
			st = "printing"
		case strings.Contains(line, "stopped") || strings.Contains(line, "disabled"):
			st = "stopped"
		}
		out = append(out, cupsDest{Name: f[1], State: st})
	}
	return out
}

func parseLpstatJobs(s string) int {
	n := 0
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "no entries") {
			continue
		}
		n++
	}
	return n
}
