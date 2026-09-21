package collect

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// oracledbConfig is collectors.modules.oracledb (sqlplus CLI, no CGO godror).
type oracledbConfig struct {
	DSN     string        `yaml:"dsn"` // user/pass@host:1521/service
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type oracledbCollector struct {
	cfg oracledbConfig
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func init() {
	Register("oracledb", func() Collector { return &oracledbCollector{} })
}

func (o *oracledbCollector) Name() string { return "oracledb" }

func (o *oracledbCollector) Configure(decode func(v any) error) error {
	if err := decode(&o.cfg); err != nil {
		return err
	}
	if o.cfg.Command == "" {
		o.cfg.Command = "sqlplus"
	}
	if o.cfg.Timeout <= 0 {
		o.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (o *oracledbCollector) Init(reg *registry.Registry) error {
	if o.cfg.Command == "" {
		if err := o.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if strings.TrimSpace(o.cfg.DSN) == "" {
		return fmt.Errorf("oracledb: no dsn")
	}
	if _, err := o.status(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "oracledb.sessions", Context: "oracledb.sessions", Title: "Sessions", Units: "sessions", Family: "sessions", Priority: 62200,
			Dimensions: []*registry.Dimension{{ID: "sessions"}}},
		{ID: "oracledb.average_active_sessions", Context: "oracledb.average_active_sessions", Title: "Average Active Sessions", Units: "sessions", Family: "sessions", Priority: 62210,
			Dimensions: []*registry.Dimension{{ID: "active"}}},
		{ID: "oracledb.sessions_utilization", Context: "oracledb.sessions_utilization", Title: "Sessions Limit %", Units: "percent", Family: "sessions", Type: registry.Area, Priority: 62220,
			Dimensions: []*registry.Dimension{{ID: "session_limit"}}},
		{ID: "oracledb.activity", Context: "oracledb.activity", Title: "Activities", Units: "events/s", Family: "activity", Priority: 62230,
			Dimensions: []*registry.Dimension{
				{ID: "parse", Algorithm: inc}, {ID: "execute", Algorithm: inc},
				{ID: "user_commits", Algorithm: inc}, {ID: "user_rollbacks", Algorithm: inc}}},
	} {
		ch.Plugin, ch.Module = "oracledb", "oracledb"
		reg.AddChart(ch)
	}
	return nil
}

func (o *oracledbCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	s, err := o.status(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("oracledb.sessions", now, map[string]float64{"sessions": s["sessions"]})
	_ = reg.Collect("oracledb.average_active_sessions", now, map[string]float64{"active": s["active"]})
	_ = reg.Collect("oracledb.sessions_utilization", now, map[string]float64{"session_limit": s["session_limit"]})
	_ = reg.Collect("oracledb.activity", now, map[string]float64{
		"parse": s["parse"], "execute": s["execute"],
		"user_commits": s["user_commits"], "user_rollbacks": s["user_rollbacks"],
	})
	return nil
}

const oracledbSQL = `SET HEADING OFF FEEDBACK OFF PAGESIZE 0
SELECT 'sessions ' || COUNT(*) FROM v$session;
SELECT 'active ' || COUNT(*) FROM v$session WHERE status='ACTIVE';
SELECT 'session_limit ' || ROUND(COUNT(*)*100/NULLIF(TO_NUMBER(p.value),0),2) FROM v$session s, v$parameter p WHERE p.name='sessions' GROUP BY p.value;
SELECT 'parse ' || value FROM v$sysstat WHERE name='parse count (total)';
SELECT 'execute ' || value FROM v$sysstat WHERE name='execute count';
SELECT 'user_commits ' || value FROM v$sysstat WHERE name='user commits';
SELECT 'user_rollbacks ' || value FROM v$sysstat WHERE name='user rollbacks';
EXIT
`

func (o *oracledbCollector) status(ctx context.Context) (map[string]float64, error) {
	run := o.run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cctx, cancel := context.WithTimeout(ctx, o.cfg.Timeout)
			defer cancel()
			cmd := exec.CommandContext(cctx, name, args...)
			cmd.Stdin = strings.NewReader(oracledbSQL)
			return cmd.Output()
		}
	}
	b, err := run(ctx, o.cfg.Command, "-s", o.cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("oracledb: %w", err)
	}
	out := parseSQLKV(b)
	if out["sessions"] == 0 && out["execute"] == 0 && out["parse"] == 0 {
		// still accept if any known key present
		if _, ok := out["sessions"]; !ok && len(out) == 0 {
			return nil, fmt.Errorf("oracledb: no metrics")
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("oracledb: no metrics")
	}
	return out, nil
}
