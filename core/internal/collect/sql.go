package collect

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// sqlConfig is collectors.modules.sql (generic CLI: mysql/psql/sqlcmd/sqlplus).
type sqlConfig struct {
	Driver  string        `yaml:"driver"` // mysql, postgres, mssql, oracle
	DSN     string        `yaml:"dsn"`
	Query   string        `yaml:"query"`
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type sqlCollector struct {
	cfg  sqlConfig
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	seen map[string]bool
}

func init() {
	Register("sql", func() Collector { return &sqlCollector{} })
}

func (s *sqlCollector) Name() string { return "sql" }

func (s *sqlCollector) Configure(decode func(v any) error) error {
	if err := decode(&s.cfg); err != nil {
		return err
	}
	if s.cfg.Driver == "" {
		s.cfg.Driver = "mysql"
	}
	if s.cfg.Command == "" {
		switch strings.ToLower(s.cfg.Driver) {
		case "postgres", "pgx", "postgresql":
			s.cfg.Command = "psql"
		case "mssql", "sqlserver":
			s.cfg.Command = "sqlcmd"
		case "oracle", "godror":
			s.cfg.Command = "sqlplus"
		default:
			s.cfg.Command = "mysql"
		}
	}
	if s.cfg.Timeout <= 0 {
		s.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (s *sqlCollector) Init(reg *registry.Registry) error {
	if s.cfg.Command == "" {
		if err := s.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if strings.TrimSpace(s.cfg.Query) == "" {
		return fmt.Errorf("sql: no query")
	}
	s.seen = map[string]bool{}
	if _, _, err := s.execQuery(context.Background()); err != nil {
		return err
	}
	return nil
}

func (s *sqlCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	vals, dur, err := s.execQuery(ctx)
	if err != nil {
		return err
	}
	drv := sanitizeID(s.cfg.Driver)
	qid := "sql." + drv + "_query"
	tid := "sql." + drv + "_query_time"
	if !s.seen[qid] {
		s.seen[qid] = true
		ch := &registry.Chart{ID: qid, Context: "sql." + drv + "_query", Title: "SQL query metrics", Units: "value",
			Family: "Query", Priority: 62300, Labels: map[string]string{"driver": s.cfg.Driver}}
		ch.Plugin, ch.Module = "sql", "sql"
		reg.AddChart(ch)
		t := &registry.Chart{ID: tid, Context: "sql." + drv + "_query_time", Title: "SQL query execution time", Units: "ms",
			Family: "Query/Timings", Priority: 62310, Labels: map[string]string{"driver": s.cfg.Driver},
			Dimensions: []*registry.Dimension{{ID: "duration"}}}
		t.Plugin, t.Module = "sql", "sql"
		reg.AddChart(t)
	}
	if ch, ok := reg.Chart(qid); ok {
		for k := range vals {
			did := sanitizeID(k)
			if ch.Dimension(did) == nil {
				ch.AddDimension(&registry.Dimension{ID: did, Name: k})
			}
		}
	}
	out := map[string]float64{}
	for k, v := range vals {
		out[sanitizeID(k)] = v
	}
	_ = reg.Collect(qid, now, out)
	_ = reg.Collect(tid, now, map[string]float64{"duration": dur})
	return nil
}

func (s *sqlCollector) execQuery(ctx context.Context) (map[string]float64, float64, error) {
	run := s.run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
			defer cancel()
			cmd := exec.CommandContext(cctx, name, args...)
			if strings.EqualFold(s.cfg.Driver, "oracle") || strings.EqualFold(s.cfg.Driver, "godror") {
				cmd.Stdin = strings.NewReader(s.cfg.Query + "\nEXIT\n")
			}
			return cmd.Output()
		}
	}
	start := time.Now()
	b, err := run(ctx, s.cfg.Command, s.queryArgs()...)
	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		return nil, dur, fmt.Errorf("sql: %w", err)
	}
	vals := parseSQLKV(b)
	if len(vals) == 0 {
		// single numeric cell
		if n := firstFloat(strings.TrimSpace(string(b))); n != 0 || strings.TrimSpace(string(b)) == "0" {
			vals = map[string]float64{"value": n}
		}
	}
	if len(vals) == 0 {
		return nil, dur, fmt.Errorf("sql: no rows")
	}
	return vals, dur, nil
}

func (s *sqlCollector) queryArgs() []string {
	extra := strings.Fields(s.cfg.DSN)
	switch strings.ToLower(s.cfg.Driver) {
	case "postgres", "pgx", "postgresql":
		return append(extra, "-t", "-A", "-F", " ", "-c", s.cfg.Query)
	case "mssql", "sqlserver":
		return append(extra, "-h", "-1", "-W", "-Q", s.cfg.Query)
	case "oracle", "godror":
		if len(extra) == 0 {
			return []string{"-s"}
		}
		return append([]string{"-s"}, extra[0])
	default:
		return append(extra, "--batch", "--raw", "-e", s.cfg.Query)
	}
}
