package collect

import (
	"context"
	"os"
	"runtime"
	"runtime/pprof"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/process"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// profileConfig is collectors.modules.profile (Netdata profile.plugin).
// Charts are the agent process itself. Stack samples stay on the profile
// function so the default path does not pay for a continuous CPU profile.
type profileConfig struct {
	Stacks bool `yaml:"stacks"`
}

type profileCollector struct {
	cfg  profileConfig
	proc *process.Process
}

func init() {
	Register("profile", func() Collector { return &profileCollector{} })
}

func (p *profileCollector) Name() string { return "profile" }

func (p *profileCollector) Configure(decode func(v any) error) error {
	return decode(&p.cfg)
}

func (p *profileCollector) Init(reg *registry.Registry) error {
	proc, err := process.NewProcess(int32(os.Getpid()))
	if err == nil {
		p.proc = proc
	}
	for _, ch := range []*registry.Chart{
		sysChart("profile.cpu", "profile", "Agent CPU time", "seconds/s", 90000, incDim("user"), incDim("system")),
		sysChart("profile.memory", "profile", "Agent memory", "bytes", 90010,
			&registry.Dimension{ID: "heap"}, &registry.Dimension{ID: "sys"}),
		sysChart("profile.goroutines", "profile", "Agent goroutines", "goroutines", 90020,
			&registry.Dimension{ID: "goroutines"}),
	} {
		ch.Plugin, ch.Module = "profile", "profile"
		reg.AddChart(ch)
	}
	if p.cfg.Stacks {
		runtime.SetMutexProfileFraction(5)
		runtime.SetBlockProfileRate(1)
		ch := sysChart("profile.stacks", "profile", "Agent profile enabled", "state", 90030,
			&registry.Dimension{ID: "enabled"})
		ch.Plugin, ch.Module = "profile", "profile"
		reg.AddChart(ch)
	}
	return nil
}

func (p *profileCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	user, sys := 0.0, 0.0
	if p.proc != nil {
		if t, err := p.proc.TimesWithContext(ctx); err == nil {
			user, sys = t.User, t.System
		}
	}
	_ = reg.Collect("profile.cpu", now, map[string]float64{"user": user, "system": sys})
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	_ = reg.Collect("profile.memory", now, map[string]float64{"heap": float64(ms.HeapAlloc), "sys": float64(ms.Sys)})
	_ = reg.Collect("profile.goroutines", now, map[string]float64{"goroutines": float64(runtime.NumGoroutine())})
	if p.cfg.Stacks {
		_ = reg.Collect("profile.stacks", now, map[string]float64{"enabled": 1})
	}
	return nil
}

func (p *profileCollector) Functions() []Function {
	return []Function{{
		Name:    "profile",
		Help:    "Agent goroutine stacks (profile.plugin)",
		Timeout: 5,
		Run: func(context.Context, map[string]string) (any, error) {
			return p.stacks(), nil
		},
	}}
}

func (p *profileCollector) stacks() Table {
	var b strings.Builder
	_ = pprof.Lookup("goroutine").WriteTo(&b, 1)
	var rows []any
	for _, line := range strings.Split(b.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rows = append(rows, map[string]string{"stack": line})
		if len(rows) >= 200 {
			break
		}
	}
	return Table{Columns: []string{"stack"}, Rows: rows, Total: len(rows)}
}
