package collect

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// as400Config is collectors.modules.as400 (ibm.d IBM i via isql/CLI, no CGO).
type as400Config struct {
	DSN      string        `yaml:"dsn"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Command  string        `yaml:"command"`
	Timeout  time.Duration `yaml:"timeout"`
}

type as400Collector struct {
	cfg as400Config
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func init() {
	Register("as400", func() Collector { return &as400Collector{} })
}

func (a *as400Collector) Name() string { return "as400" }

func (a *as400Collector) Configure(decode func(v any) error) error {
	if err := decode(&a.cfg); err != nil {
		return err
	}
	if a.cfg.Command == "" {
		a.cfg.Command = "isql"
	}
	if a.cfg.Timeout <= 0 {
		a.cfg.Timeout = 8 * time.Second
	}
	return nil
}

func (a *as400Collector) Init(reg *registry.Registry) error {
	if a.cfg.Command == "" {
		if err := a.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if strings.TrimSpace(a.cfg.DSN) == "" {
		return fmt.Errorf("as400: no dsn")
	}
	if _, err := a.status(context.Background()); err != nil {
		return err
	}
	for _, ch := range []*registry.Chart{
		{ID: "as400.cpu_utilization", Context: "as400.cpu_utilization", Title: "CPU utilization", Units: "percentage", Family: "cpu", Type: registry.Area, Priority: 64200,
			Dimensions: []*registry.Dimension{{ID: "utilization"}}},
		{ID: "as400.cpu_configuration", Context: "as400.cpu_configuration", Title: "Configured CPUs", Units: "cpus", Family: "cpu", Priority: 64210,
			Dimensions: []*registry.Dimension{{ID: "configured"}}},
		{ID: "as400.total_jobs", Context: "as400.total_jobs", Title: "Total jobs", Units: "jobs", Family: "jobs", Priority: 64220,
			Dimensions: []*registry.Dimension{{ID: "total"}}},
		{ID: "as400.active_jobs_by_type", Context: "as400.active_jobs_by_type", Title: "Active jobs by type", Units: "jobs", Family: "jobs", Type: registry.Stacked, Priority: 64230,
			Dimensions: []*registry.Dimension{{ID: "batch"}, {ID: "interactive"}, {ID: "active"}}},
		{ID: "as400.job_queue_length", Context: "as400.job_queue_length", Title: "Job queue length", Units: "jobs", Family: "jobs", Priority: 64240,
			Dimensions: []*registry.Dimension{{ID: "waiting"}}},
		{ID: "as400.system_asp_usage", Context: "as400.system_asp_usage", Title: "System ASP usage", Units: "percentage", Family: "storage", Type: registry.Area, Priority: 64250,
			Dimensions: []*registry.Dimension{{ID: "used"}}},
		{ID: "as400.network_connections", Context: "as400.network_connections", Title: "Network connections", Units: "connections", Family: "network", Priority: 64260,
			Dimensions: []*registry.Dimension{{ID: "remote"}, {ID: "total"}}},
	} {
		ch.Plugin, ch.Module = "ibm.d", "as400"
		reg.AddChart(ch)
	}
	return nil
}

func (a *as400Collector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	s, err := a.status(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("as400.cpu_utilization", now, map[string]float64{"utilization": s["utilization"]})
	_ = reg.Collect("as400.cpu_configuration", now, map[string]float64{"configured": s["configured"]})
	_ = reg.Collect("as400.total_jobs", now, map[string]float64{"total": s["total"]})
	_ = reg.Collect("as400.active_jobs_by_type", now, map[string]float64{
		"batch": s["batch"], "interactive": s["interactive"], "active": s["active"],
	})
	_ = reg.Collect("as400.job_queue_length", now, map[string]float64{"waiting": s["waiting"]})
	_ = reg.Collect("as400.system_asp_usage", now, map[string]float64{"used": s["used"]})
	_ = reg.Collect("as400.network_connections", now, map[string]float64{
		"remote": s["remote"], "total": s["net_total"],
	})
	return nil
}

const as400SQL = `SELECT 'utilization', AVERAGE_CPU_UTILIZATION FROM TABLE(QSYS2.SYSTEM_STATUS()) X;
SELECT 'configured', CONFIGURED_CPUS FROM TABLE(QSYS2.SYSTEM_STATUS()) X;
SELECT 'total', TOTAL_JOBS_IN_SYSTEM FROM TABLE(QSYS2.SYSTEM_STATUS()) X;
SELECT 'batch', BATCH_JOBS FROM TABLE(QSYS2.SYSTEM_STATUS()) X;
SELECT 'interactive', INTERACTIVE_JOBS FROM TABLE(QSYS2.SYSTEM_STATUS()) X;
SELECT 'active', ACTIVE_JOBS FROM TABLE(QSYS2.SYSTEM_STATUS()) X;
SELECT 'waiting', JOBS_WAITING FROM TABLE(QSYS2.SYSTEM_STATUS()) X;
SELECT 'used', SYSTEM_ASP_USED FROM TABLE(QSYS2.SYSTEM_STATUS()) X;
SELECT 'remote', REMOTE_CONNECTIONS FROM TABLE(QSYS2.SYSTEM_STATUS()) X;
SELECT 'net_total', TOTAL_CONNECTIONS FROM TABLE(QSYS2.SYSTEM_STATUS()) X;
`

func (a *as400Collector) status(ctx context.Context) (map[string]float64, error) {
	run := a.run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cctx, cancel := context.WithTimeout(ctx, a.cfg.Timeout)
			defer cancel()
			cmd := exec.CommandContext(cctx, name, args...)
			cmd.Stdin = strings.NewReader(as400SQL)
			return cmd.Output()
		}
	}
	args := []string{"-b", "-d", a.cfg.DSN}
	if a.cfg.User != "" {
		args = append(args, a.cfg.User)
		if a.cfg.Password != "" {
			args = append(args, a.cfg.Password)
		}
	}
	b, err := run(ctx, a.cfg.Command, args...)
	if err != nil {
		return nil, fmt.Errorf("as400: %w", err)
	}
	out := parseSQLKV(b)
	if len(out) == 0 {
		return nil, fmt.Errorf("as400: no metrics")
	}
	if v, ok := out["average_cpu_utilization"]; ok {
		out["utilization"] = v
	}
	if v, ok := out["total_jobs"]; ok {
		out["total"] = v
	}
	return out, nil
}
