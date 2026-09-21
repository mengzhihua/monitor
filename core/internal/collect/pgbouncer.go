package collect

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// pgbouncerConfig is collectors.modules.pgbouncer (Netdata go.d pgbouncer).
type pgbouncerConfig struct {
	Address  string        `yaml:"address"` // host:port or unix:///dir
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type pgbouncerCollector struct {
	cfg    pgbouncerConfig
	dbSeen map[string]bool
}

func init() {
	Register("pgbouncer", func() Collector { return &pgbouncerCollector{} })
}

func (p *pgbouncerCollector) Name() string { return "pgbouncer" }

func (p *pgbouncerCollector) Configure(decode func(v any) error) error {
	if err := decode(&p.cfg); err != nil {
		return err
	}
	if p.cfg.Address == "" {
		p.cfg.Address = "127.0.0.1:6432"
	}
	if p.cfg.User == "" {
		p.cfg.User = "pgbouncer"
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (p *pgbouncerCollector) Init(reg *registry.Registry) error {
	if p.cfg.Address == "" {
		if err := p.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	p.dbSeen = map[string]bool{}
	if _, err := p.show(context.Background(), "SHOW POOLS"); err != nil {
		return err
	}
	for _, c := range []*registry.Chart{
		{ID: "pgbouncer.client_connections", Title: "PgBouncer client connections", Units: "connections", Type: registry.Stacked, Priority: 47100,
			Dimensions: []*registry.Dimension{{ID: "active"}, {ID: "waiting"}}},
		{ID: "pgbouncer.server_connections", Title: "PgBouncer server connections", Units: "connections", Type: registry.Stacked, Priority: 47110,
			Dimensions: []*registry.Dimension{{ID: "active"}, {ID: "idle"}, {ID: "used"}, {ID: "login"}}},
		{ID: "pgbouncer.queries", Title: "PgBouncer queries", Units: "queries/s", Priority: 47120,
			Dimensions: []*registry.Dimension{{ID: "queries", Algorithm: registry.Incremental}}},
		{ID: "pgbouncer.network_io", Title: "PgBouncer traffic", Units: "bytes/s", Type: registry.Area, Priority: 47130,
			Dimensions: []*registry.Dimension{
				{ID: "received", Algorithm: registry.Incremental},
				{ID: "sent", Algorithm: registry.Incremental, Multiplier: -1}}},
	} {
		c.Family, c.Plugin, c.Module = "pgbouncer", "pgbouncer", "pgbouncer"
		reg.AddChart(c)
	}
	return nil
}

func (p *pgbouncerCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	pools, err := p.show(ctx, "SHOW POOLS")
	if err != nil {
		return err
	}
	stats, err := p.show(ctx, "SHOW STATS")
	if err != nil {
		return err
	}
	var clAct, clWait, svAct, svIdle, svUsed, svLogin float64
	for _, row := range pools {
		db := row["database"]
		if db == "" || db == "pgbouncer" {
			continue
		}
		clAct += rowF(row, "cl_active")
		clWait += rowF(row, "cl_waiting")
		svAct += rowF(row, "sv_active")
		svIdle += rowF(row, "sv_idle")
		svUsed += rowF(row, "sv_used")
		svLogin += rowF(row, "sv_login")
		p.ensureDB(reg, db)
		id := sanitizeID(db)
		_ = reg.Collect("pgbouncer.db_client_connections."+id, now, map[string]float64{
			"active": rowF(row, "cl_active"), "waiting": rowF(row, "cl_waiting"), "cancel_req": rowF(row, "cl_cancel_req")})
		_ = reg.Collect("pgbouncer.db_server_connections."+id, now, map[string]float64{
			"active": rowF(row, "sv_active"), "idle": rowF(row, "sv_idle"),
			"used": rowF(row, "sv_used"), "tested": rowF(row, "sv_tested"), "login": rowF(row, "sv_login")})
	}
	var queries, recv, sent float64
	for _, row := range stats {
		if row["database"] == "pgbouncer" {
			continue
		}
		queries += rowF(row, "total_query_count")
		if queries == 0 {
			queries += rowF(row, "avg_query_count") // older pgbouncer
		}
		recv += rowF(row, "total_received")
		sent += rowF(row, "total_sent")
	}
	_ = reg.Collect("pgbouncer.client_connections", now, map[string]float64{"active": clAct, "waiting": clWait})
	_ = reg.Collect("pgbouncer.server_connections", now, map[string]float64{
		"active": svAct, "idle": svIdle, "used": svUsed, "login": svLogin})
	_ = reg.Collect("pgbouncer.queries", now, map[string]float64{"queries": queries})
	_ = reg.Collect("pgbouncer.network_io", now, map[string]float64{"received": recv, "sent": sent})
	return nil
}

func (p *pgbouncerCollector) ensureDB(reg *registry.Registry, db string) {
	if p.dbSeen[db] {
		return
	}
	p.dbSeen[db] = true
	id := sanitizeID(db)
	lbl := map[string]string{"database": db}
	reg.AddChart(&registry.Chart{ID: "pgbouncer.db_client_connections." + id, Context: "pgbouncer.db_client_connections",
		Family: "pgbouncer", Title: "PgBouncer client connections " + db, Units: "connections", Type: registry.Stacked,
		Priority: 47140, Plugin: "pgbouncer", Module: "pgbouncer", Labels: lbl,
		Dimensions: []*registry.Dimension{{ID: "active"}, {ID: "waiting"}, {ID: "cancel_req"}}})
	reg.AddChart(&registry.Chart{ID: "pgbouncer.db_server_connections." + id, Context: "pgbouncer.db_server_connections",
		Family: "pgbouncer", Title: "PgBouncer server connections " + db, Units: "connections", Type: registry.Stacked,
		Priority: 47141, Plugin: "pgbouncer", Module: "pgbouncer", Labels: lbl,
		Dimensions: []*registry.Dimension{{ID: "active"}, {ID: "idle"}, {ID: "used"}, {ID: "tested"}, {ID: "login"}}})
}

func (p *pgbouncerCollector) show(ctx context.Context, sql string) ([]map[string]string, error) {
	addr := p.cfg.Address
	if strings.HasPrefix(addr, "unix://") {
		path := strings.TrimPrefix(addr, "unix://")
		if !strings.Contains(path, ".s.PGSQL") {
			path = strings.TrimRight(path, "/") + "/.s.PGSQL.6432"
			addr = "unix://" + path
		}
	}
	return pgQueryMaps(ctx, addr, p.cfg.User, p.cfg.Password, "pgbouncer", p.cfg.Timeout, sql)
}

func rowF(row map[string]string, k string) float64 {
	v, _ := strconv.ParseFloat(row[k], 64)
	return v
}

// pgQueryMaps is pgQuery that keeps column names from RowDescription.
func pgQueryMaps(ctx context.Context, address, user, password, database string, timeout time.Duration, sql string) ([]map[string]string, error) {
	rows, cols, err := pgQueryWithCols(ctx, address, user, password, database, timeout, sql)
	if err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("pgbouncer: no columns")
	}
	var out []map[string]string
	for _, r := range rows {
		m := map[string]string{}
		for i, c := range cols {
			if i < len(r) {
				m[c] = r[i]
			}
		}
		out = append(out, m)
	}
	return out, nil
}
