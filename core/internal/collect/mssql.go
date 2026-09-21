package collect

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// mssqlConfig is collectors.modules.mssql (sqlcmd CLI, no go-mssqldb).
type mssqlConfig struct {
	Address  string        `yaml:"address"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Command  string        `yaml:"command"`
	Timeout  time.Duration `yaml:"timeout"`
}

type mssqlCollector struct {
	cfg mssqlConfig
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func init() {
	Register("mssql", func() Collector { return &mssqlCollector{} })
}

func (m *mssqlCollector) Name() string { return "mssql" }

func (m *mssqlCollector) Configure(decode func(v any) error) error {
	if err := decode(&m.cfg); err != nil {
		return err
	}
	if m.cfg.Address == "" {
		m.cfg.Address = "127.0.0.1,1433"
	}
	if m.cfg.User == "" {
		m.cfg.User = "sa"
	}
	if m.cfg.Command == "" {
		m.cfg.Command = "sqlcmd"
	}
	if m.cfg.Timeout <= 0 {
		m.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (m *mssqlCollector) Init(reg *registry.Registry) error {
	if m.cfg.Command == "" {
		if err := m.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := m.status(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "mssql.user_connections", Context: "mssql.user_connections", Title: "User connections", Units: "connections", Family: "connections", Priority: 62100,
			Dimensions: []*registry.Dimension{{ID: "user"}}},
		{ID: "mssql.session_connections", Context: "mssql.session_connections", Title: "Session connections", Units: "connections", Family: "connections", Priority: 62110,
			Dimensions: []*registry.Dimension{{ID: "user"}, {ID: "internal"}}},
		{ID: "mssql.blocked_processes", Context: "mssql.blocked_processes", Title: "Blocked processes", Units: "processes", Family: "processes", Priority: 62120,
			Dimensions: []*registry.Dimension{{ID: "blocked"}}},
		{ID: "mssql.batch_requests", Context: "mssql.batch_requests", Title: "Batch requests", Units: "requests/s", Family: "queries", Priority: 62130,
			Dimensions: []*registry.Dimension{{ID: "batch", Algorithm: inc}}},
		{ID: "mssql.compilations", Context: "mssql.compilations", Title: "SQL compilations", Units: "compilations/s", Family: "queries", Priority: 62140,
			Dimensions: []*registry.Dimension{{ID: "compilations", Algorithm: inc}}},
		{ID: "mssql.recompilations", Context: "mssql.recompilations", Title: "SQL re-compilations", Units: "recompilations/s", Family: "queries", Priority: 62150,
			Dimensions: []*registry.Dimension{{ID: "recompilations", Algorithm: inc}}},
		{ID: "mssql.buffer_cache_hit_ratio", Context: "mssql.buffer_cache_hit_ratio", Title: "Buffer cache hit ratio", Units: "percent", Family: "buffer", Priority: 62160,
			Dimensions: []*registry.Dimension{{ID: "hit_ratio"}}},
	} {
		ch.Plugin, ch.Module = "mssql", "mssql"
		reg.AddChart(ch)
	}
	return nil
}

func (m *mssqlCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	s, err := m.status(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("mssql.user_connections", now, map[string]float64{"user": s["user_connections"]})
	_ = reg.Collect("mssql.session_connections", now, map[string]float64{"user": s["session_user"], "internal": s["session_internal"]})
	_ = reg.Collect("mssql.blocked_processes", now, map[string]float64{"blocked": s["blocked_processes"]})
	_ = reg.Collect("mssql.batch_requests", now, map[string]float64{"batch": s["batch_requests"]})
	_ = reg.Collect("mssql.compilations", now, map[string]float64{"compilations": s["sql_compilations"]})
	_ = reg.Collect("mssql.recompilations", now, map[string]float64{"recompilations": s["sql_recompilations"]})
	_ = reg.Collect("mssql.buffer_cache_hit_ratio", now, map[string]float64{"hit_ratio": s["buffer_cache_hit_ratio"]})
	return nil
}

const mssqlQuery = `SET NOCOUNT ON; SELECT RTRIM(counter_name), cntr_value FROM sys.dm_os_performance_counters WHERE RTRIM(counter_name) IN ('User Connections','Processes blocked','Batch Requests/sec','SQL Compilations/sec','SQL Re-Compilations/sec','Buffer cache hit ratio','User connection count','Internal connection count');`

func (m *mssqlCollector) status(ctx context.Context) (map[string]float64, error) {
	run := m.run
	if run == nil {
		run = execRun(m.cfg.Timeout)
	}
	args := []string{"-S", m.cfg.Address, "-U", m.cfg.User, "-P", m.cfg.Password, "-h", "-1", "-W", "-Q", mssqlQuery}
	b, err := run(ctx, m.cfg.Command, args...)
	if err != nil {
		return nil, fmt.Errorf("mssql: %w", err)
	}
	out := parseSQLKV(b)
	if len(out) == 0 {
		return nil, fmt.Errorf("mssql: no counters")
	}
	alias := map[string]string{
		"user connections":          "user_connections",
		"processes blocked":         "blocked_processes",
		"batch requests/sec":        "batch_requests",
		"sql compilations/sec":      "sql_compilations",
		"sql re-compilations/sec":   "sql_recompilations",
		"buffer cache hit ratio":    "buffer_cache_hit_ratio",
		"user connection count":     "session_user",
		"internal connection count": "session_internal",
		"user_connections":          "user_connections",
		"blocked_processes":         "blocked_processes",
		"batch_requests":            "batch_requests",
		"sql_compilations":          "sql_compilations",
		"sql_recompilations":        "sql_recompilations",
		"buffer_cache_hit_ratio":    "buffer_cache_hit_ratio",
		"session_user":              "session_user",
		"session_internal":          "session_internal",
	}
	norm := map[string]float64{}
	for k, v := range out {
		if n, ok := alias[strings.ToLower(strings.TrimSpace(k))]; ok {
			norm[n] = v
		}
	}
	if len(norm) == 0 {
		return nil, fmt.Errorf("mssql: no known counters")
	}
	return norm, nil
}

func parseSQLKV(b []byte) map[string]float64 {
	out := map[string]float64{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "-") || strings.EqualFold(line, "rows affected") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.Join(fields[:len(fields)-1], " ")
		out[key] = firstFloat(fields[len(fields)-1])
	}
	return out
}
