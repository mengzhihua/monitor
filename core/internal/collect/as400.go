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
		{ID: "as400.memory_pool_usage", Context: "as400.memory_pool_usage", Title: "Memory Pool Usage", Units: "bytes", Family: "memory", Type: registry.Stacked, Priority: 64270,
			Dimensions: []*registry.Dimension{{ID: "machine"}, {ID: "base"}, {ID: "interactive"}, {ID: "spool"}}},
		{ID: "as400.temporary_storage", Context: "as400.temporary_storage", Title: "Temporary Storage", Units: "MiB", Family: "storage", Priority: 64280,
			Dimensions: []*registry.Dimension{{ID: "current"}, {ID: "maximum"}}},
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
	for k, v := range a.memoryPools(ctx) {
		s[k] = v
	}
	_ = reg.Collect("as400.memory_pool_usage", now, map[string]float64{
		"machine": s["pool_machine"], "base": s["pool_base"],
		"interactive": s["pool_interactive"], "spool": s["pool_spool"],
	})
	_ = reg.Collect("as400.temporary_storage", now, map[string]float64{
		"current": s["temp_current"], "maximum": s["temp_maximum"],
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
SELECT 'temp_current', CURRENT_TEMPORARY_STORAGE FROM TABLE(QSYS2.SYSTEM_STATUS()) X;
SELECT 'temp_maximum', MAXIMUM_TEMPORARY_STORAGE FROM TABLE(QSYS2.SYSTEM_STATUS()) X;
`

// Named memory pools are rows in MEMORY_POOL_INFO, not columns on SYSTEM_STATUS.
const as400PoolSQL = `SELECT 'pool_machine', CURRENT_SIZE FROM QSYS2.MEMORY_POOL_INFO WHERE POOL_NAME = '*MACHINE';
SELECT 'pool_base', CURRENT_SIZE FROM QSYS2.MEMORY_POOL_INFO WHERE POOL_NAME = '*BASE';
SELECT 'pool_interactive', CURRENT_SIZE FROM QSYS2.MEMORY_POOL_INFO WHERE POOL_NAME IN ('*INTERACT', '*INTERACTIVE');
SELECT 'pool_spool', CURRENT_SIZE FROM QSYS2.MEMORY_POOL_INFO WHERE POOL_NAME = '*SPOOL';
`

func (a *as400Collector) isqlArgs() []string {
	args := []string{"-b", "-d", a.cfg.DSN}
	if a.cfg.User != "" {
		args = append(args, a.cfg.User)
		if a.cfg.Password != "" {
			args = append(args, a.cfg.Password)
		}
	}
	return args
}

func (a *as400Collector) execSQL(ctx context.Context, sql string) ([]byte, error) {
	if a.run != nil {
		return a.run(ctx, a.cfg.Command, a.isqlArgs()...)
	}
	cctx, cancel := context.WithTimeout(ctx, a.cfg.Timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, a.cfg.Command, a.isqlArgs()...)
	cmd.WaitDelay = execWaitDelay
	cmd.Stdin = strings.NewReader(sql)
	return cmd.Output()
}

func (a *as400Collector) status(ctx context.Context) (map[string]float64, error) {
	b, err := a.execSQL(ctx, as400SQL)
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
	alias := map[string]string{
		"current_temporary_storage": "temp_current",
		"maximum_temporary_storage": "temp_maximum",
	}
	for k, dst := range alias {
		if v, ok := out[k]; ok {
			if _, have := out[dst]; !have {
				out[dst] = v
			}
		}
	}
	return out, nil
}

func (a *as400Collector) memoryPools(ctx context.Context) map[string]float64 {
	b, err := a.execSQL(ctx, as400PoolSQL)
	if err != nil {
		return nil
	}
	return parseMemoryPools(b)
}

func parseMemoryPools(b []byte) map[string]float64 {
	out := map[string]float64{}
	for k, v := range parseSQLKV(b) {
		raw := strings.TrimSpace(k)
		lk := strings.ToLower(strings.TrimPrefix(raw, "*"))
		fromStar := strings.HasPrefix(raw, "*")
		fromPool := strings.HasPrefix(lk, "pool_")
		if !fromStar && !fromPool {
			continue
		}
		switch strings.TrimPrefix(lk, "pool_") {
		case "machine":
			out["pool_machine"] = v
		case "base":
			out["pool_base"] = v
		case "interact", "interactive":
			out["pool_interactive"] = v
		case "spool":
			out["pool_spool"] = v
		}
	}
	return out
}
