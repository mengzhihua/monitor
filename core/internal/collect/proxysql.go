package collect

import (
	"context"
	"fmt"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// proxysqlConfig is collectors.modules.proxysql (MySQL admin interface).
type proxysqlConfig struct {
	Address  string        `yaml:"address"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type proxysqlCollector struct {
	cfg   proxysqlConfig
	query func(ctx context.Context, sql string) ([][]string, error)
}

func init() {
	Register("proxysql", func() Collector { return &proxysqlCollector{} })
}

func (p *proxysqlCollector) Name() string { return "proxysql" }

func (p *proxysqlCollector) Configure(decode func(v any) error) error {
	if err := decode(&p.cfg); err != nil {
		return err
	}
	if p.cfg.Address == "" {
		p.cfg.Address = "127.0.0.1:6032"
	}
	if p.cfg.User == "" {
		p.cfg.User = "stats"
	}
	if p.cfg.Password == "" {
		p.cfg.Password = "stats"
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (p *proxysqlCollector) Init(reg *registry.Registry) error {
	if p.cfg.Address == "" {
		if err := p.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := p.status(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "proxysql.client_connections_count", Title: "Client Connections", Units: "connections", Priority: 55000,
			Dimensions: []*registry.Dimension{{ID: "connected"}, {ID: "non_idle"}, {ID: "hostgroup_locked"}}},
		{ID: "proxysql.questions", Title: "Questions", Units: "questions/s", Priority: 55010,
			Dimensions: []*registry.Dimension{{ID: "questions", Algorithm: inc}}},
		{ID: "proxysql.slow_queries", Title: "Slow Queries", Units: "queries/s", Priority: 55020,
			Dimensions: []*registry.Dimension{{ID: "slow_queries", Algorithm: inc}}},
		{ID: "proxysql.commands", Title: "Commands", Units: "commands/s", Type: registry.Stacked, Priority: 55030,
			Dimensions: []*registry.Dimension{
				{ID: "stmt_prepare", Algorithm: inc}, {ID: "stmt_execute", Algorithm: inc}, {ID: "stmt_close", Algorithm: inc}}},
	} {
		c.Family, c.Plugin, c.Module = "proxysql", "proxysql", "proxysql"
		reg.AddChart(c)
	}
	return nil
}

func (p *proxysqlCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := p.status(ctx)
	if err != nil {
		return err
	}
	g := func(k string) float64 { return firstFloat(m[k]) }
	_ = reg.Collect("proxysql.client_connections_count", now, map[string]float64{
		"connected": g("Client_Connections_connected"), "non_idle": g("Client_Connections_non_idle"),
		"hostgroup_locked": g("Client_Connections_hostgroup_locked")})
	_ = reg.Collect("proxysql.questions", now, map[string]float64{"questions": g("Questions")})
	_ = reg.Collect("proxysql.slow_queries", now, map[string]float64{"slow_queries": g("Slow_queries")})
	_ = reg.Collect("proxysql.commands", now, map[string]float64{
		"stmt_prepare": g("Com_stmt_prepare"), "stmt_execute": g("Com_stmt_execute"), "stmt_close": g("Com_stmt_close")})
	return nil
}

func (p *proxysqlCollector) status(ctx context.Context) (map[string]string, error) {
	q := p.query
	if q == nil {
		q = func(ctx context.Context, sql string) ([][]string, error) {
			return mysqlQuery(ctx, p.cfg.Address, p.cfg.User, p.cfg.Password, p.cfg.Timeout, sql)
		}
	}
	rows, err := q(ctx, "SELECT Variable_Name, Variable_Value FROM stats_mysql_global")
	if err != nil {
		rows, err = q(ctx, "SHOW MYSQL STATUS")
		if err != nil {
			return nil, fmt.Errorf("proxysql: %w", err)
		}
	}
	out := map[string]string{}
	for _, r := range rows {
		if len(r) >= 2 {
			out[r[0]] = r[1]
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("proxysql: empty status")
	}
	return out, nil
}
