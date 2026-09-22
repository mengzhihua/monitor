package collect

import (
	"bufio"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// xenstatConfig is collectors.modules.xenstat (Netdata xenstat.plugin via xl list).
type xenstatConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type xenstatCollector struct {
	cfg  xenstatConfig
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	seen map[string]bool
}

func init() {
	Register("xenstat", func() Collector { return &xenstatCollector{} })
}

func (x *xenstatCollector) Name() string { return "xenstat" }

func (x *xenstatCollector) Configure(decode func(v any) error) error {
	if err := decode(&x.cfg); err != nil {
		return err
	}
	if x.cfg.Command == "" {
		x.cfg.Command = "xl"
	}
	if x.cfg.Timeout <= 0 {
		x.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (x *xenstatCollector) Init(reg *registry.Registry) error {
	if x.cfg.Command == "" {
		if err := x.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if x.run == nil {
		x.run = execRun(x.cfg.Timeout)
	}
	raw, err := x.run(context.Background(), x.cfg.Command, "list")
	if err != nil {
		return fmt.Errorf("xenstat: xl unavailable: %w", err)
	}
	if len(parseXLList(string(raw))) == 0 {
		return fmt.Errorf("xenstat: no xen domains")
	}
	x.seen = map[string]bool{}
	ch := sysChart("xen.domains", "xen", "Xen domains", "domains", 37000, &registry.Dimension{ID: "running"})
	ch.Plugin, ch.Module, ch.Family = "xenstat", "xenstat", "xen"
	reg.AddChart(ch)
	return nil
}

func (x *xenstatCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	raw, err := x.run(ctx, x.cfg.Command, "list")
	if err != nil {
		return err
	}
	doms := parseXLList(string(raw))
	_ = reg.Collect("xen.domains", now, map[string]float64{"running": float64(len(doms))})
	for _, d := range doms {
		id := sanitizeID(d.Name)
		if !x.seen[id] {
			x.seen[id] = true
			cpu := sysChart("xen.cpu."+id, "xen", "Xen CPU "+d.Name, "seconds", 37010, incDim("cpu"))
			cpu.Plugin, cpu.Module, cpu.Context = "xenstat", "xenstat", "xen.cpu"
			reg.AddChart(cpu)
			mem := sysChart("xen.mem."+id, "xen", "Xen memory "+d.Name, "MiB", 37020, &registry.Dimension{ID: "mem"})
			mem.Plugin, mem.Module, mem.Context = "xenstat", "xenstat", "xen.mem"
			reg.AddChart(mem)
			st := sysChart("xen.state."+id, "xen", "Xen domain "+d.Name+" running", "boolean", 37005,
				&registry.Dimension{ID: "running"})
			st.Plugin, st.Module, st.Context = "xenstat", "xenstat", "xen.state"
			reg.AddChart(st)
		}
		running := 0.0
		if strings.Contains(d.State, "r") {
			running = 1
		}
		_ = reg.Collect("xen.cpu."+id, now, map[string]float64{"cpu": d.Time})
		_ = reg.Collect("xen.mem."+id, now, map[string]float64{"mem": d.Mem})
		_ = reg.Collect("xen.state."+id, now, map[string]float64{"running": running})
	}
	return nil
}

type xlDomain struct {
	Name, State          string
	ID, Mem, VCPUs, Time float64
}

func parseXLList(s string) []xlDomain {
	var out []xlDomain
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "Name") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		id, _ := strconv.ParseFloat(f[len(f)-5], 64)
		mem, _ := strconv.ParseFloat(f[len(f)-4], 64)
		vcpu, _ := strconv.ParseFloat(f[len(f)-3], 64)
		st := f[len(f)-2]
		tm, _ := strconv.ParseFloat(f[len(f)-1], 64)
		name := strings.Join(f[:len(f)-5], "_")
		out = append(out, xlDomain{Name: name, ID: id, Mem: mem, VCPUs: vcpu, State: st, Time: tm})
	}
	return out
}
