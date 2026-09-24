package collect

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// db2Config is collectors.modules.db2 (ibm.d subset via CLI, no CGO ODBC).
type db2Config struct {
	DSN      string        `yaml:"dsn"` // alias or "user/pass@db"
	Command  string        `yaml:"command"`
	Database string        `yaml:"database"`
	Timeout  time.Duration `yaml:"timeout"`
}

type db2Collector struct {
	cfg db2Config
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func init() {
	Register("db2", func() Collector { return &db2Collector{} })
}

func (d *db2Collector) Name() string { return "db2" }

func (d *db2Collector) Configure(decode func(v any) error) error {
	if err := decode(&d.cfg); err != nil {
		return err
	}
	if d.cfg.Command == "" {
		d.cfg.Command = "db2"
	}
	if d.cfg.Timeout <= 0 {
		d.cfg.Timeout = 8 * time.Second
	}
	return nil
}

func (d *db2Collector) Init(reg *registry.Registry) error {
	if d.cfg.Command == "" {
		if err := d.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if strings.TrimSpace(d.cfg.DSN) == "" && strings.TrimSpace(d.cfg.Database) == "" {
		return fmt.Errorf("db2: no dsn")
	}
	if _, err := d.status(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "db2.connections", Context: "db2.connections", Title: "Database Connections", Units: "connections", Family: "connections", Priority: 64100,
			Dimensions: []*registry.Dimension{{ID: "total"}, {ID: "active"}, {ID: "idle"}}},
		{ID: "db2.database_applications", Context: "db2.database_applications", Title: "Database Applications", Units: "applications", Family: "connections", Priority: 64110,
			Dimensions: []*registry.Dimension{{ID: "applications"}}},
		{ID: "db2.locking", Context: "db2.locking", Title: "Database Locking", Units: "events/s", Family: "locks", Priority: 64120,
			Dimensions: []*registry.Dimension{
				{ID: "waits", Algorithm: inc}, {ID: "timeouts", Algorithm: inc}, {ID: "escalations", Algorithm: inc}}},
		{ID: "db2.deadlocks", Context: "db2.deadlocks", Title: "Database Deadlocks", Units: "deadlocks/s", Family: "locks", Priority: 64130,
			Dimensions: []*registry.Dimension{{ID: "deadlocks", Algorithm: inc}}},
		{ID: "db2.log_utilization", Context: "db2.log_utilization", Title: "Transaction Log Utilization", Units: "percentage", Family: "log", Type: registry.Area, Priority: 64140,
			Dimensions: []*registry.Dimension{{ID: "utilization"}}},
		{ID: "db2.log_space", Context: "db2.log_space", Title: "Transaction Log Space", Units: "bytes", Family: "log", Type: registry.Stacked, Priority: 64141,
			Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "available"}}},
		{ID: "db2.bufferpool_hit_ratio", Context: "db2.bufferpool_hit_ratio", Title: "Bufferpool Hit Ratio", Units: "percentage", Family: "bufferpool", Type: registry.Stacked, Priority: 64145,
			Dimensions: []*registry.Dimension{{ID: "hits"}, {ID: "misses"}}},
		{ID: "db2.service_health", Context: "db2.service_health", Title: "Service Health Status", Units: "status", Family: "health", Priority: 64150,
			Dimensions: []*registry.Dimension{{ID: "connection"}, {ID: "database"}}},
	} {
		ch.Plugin, ch.Module = "ibm.d", "db2"
		reg.AddChart(ch)
	}
	return nil
}

func (d *db2Collector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	s, err := d.status(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("db2.connections", now, map[string]float64{
		"total": s["total"], "active": s["active"], "idle": s["idle"],
	})
	apps := s["applications"]
	if apps == 0 {
		apps = s["total"]
	}
	_ = reg.Collect("db2.database_applications", now, map[string]float64{"applications": apps})
	_ = reg.Collect("db2.locking", now, map[string]float64{
		"waits": s["waits"], "timeouts": s["timeouts"], "escalations": s["escalations"],
	})
	_ = reg.Collect("db2.deadlocks", now, map[string]float64{"deadlocks": s["deadlocks"]})
	util := s["utilization"]
	if util == 0 {
		util = s["used"]
	}
	_ = reg.Collect("db2.log_utilization", now, map[string]float64{"utilization": util})
	logUsed := s["log_used"]
	logAvail := s["log_avail"]
	if logUsed == 0 {
		logUsed = s["total_log_used"]
	}
	if logAvail == 0 {
		logAvail = s["total_log_available"]
	}
	_ = reg.Collect("db2.log_space", now, map[string]float64{"used": logUsed, "available": logAvail})
	hits, misses := s["hits"], s["misses"]
	if tot := hits + misses; tot > 0 {
		hits, misses = hits*100/tot, misses*100/tot
	}
	_ = reg.Collect("db2.bufferpool_hit_ratio", now, map[string]float64{"hits": hits, "misses": misses})
	_ = reg.Collect("db2.service_health", now, map[string]float64{"connection": 1, "database": 1})
	return nil
}

const db2SQL = `SELECT 'total ' || TRIM(CHAR(APPLS_CUR_CONS)) FROM TABLE(MON_GET_DATABASE(-2)) AS t;
SELECT 'active ' || TRIM(CHAR(APPLS_IN_DB2)) FROM TABLE(MON_GET_DATABASE(-2)) AS t;
SELECT 'idle ' || TRIM(CHAR(APPLS_CUR_CONS-APPLS_IN_DB2)) FROM TABLE(MON_GET_DATABASE(-2)) AS t;
SELECT 'waits ' || TRIM(CHAR(LOCK_WAITS)) FROM TABLE(MON_GET_DATABASE(-2)) AS t;
SELECT 'timeouts ' || TRIM(CHAR(LOCK_TIMEOUTS)) FROM TABLE(MON_GET_DATABASE(-2)) AS t;
SELECT 'escalations ' || TRIM(CHAR(LOCK_ESCALS)) FROM TABLE(MON_GET_DATABASE(-2)) AS t;
SELECT 'deadlocks ' || TRIM(CHAR(DEADLOCKS)) FROM TABLE(MON_GET_DATABASE(-2)) AS t;
SELECT 'utilization ' || TRIM(CHAR(TOTAL_LOG_USED*100.0/NULLIF(TOTAL_LOG_AVAILABLE+TOTAL_LOG_USED,0))) FROM TABLE(MON_GET_DATABASE(-2)) AS t;
SELECT 'log_used ' || TRIM(CHAR(TOTAL_LOG_USED)) FROM TABLE(MON_GET_DATABASE(-2)) AS t;
SELECT 'log_avail ' || TRIM(CHAR(TOTAL_LOG_AVAILABLE)) FROM TABLE(MON_GET_DATABASE(-2)) AS t;
SELECT 'hits ' || TRIM(CHAR(POOL_DATA_L_READS-POOL_DATA_P_READS)) FROM TABLE(MON_GET_DATABASE(-2)) AS t;
SELECT 'misses ' || TRIM(CHAR(POOL_DATA_P_READS)) FROM TABLE(MON_GET_DATABASE(-2)) AS t;
`

func (d *db2Collector) status(ctx context.Context) (map[string]float64, error) {
	run := d.run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cctx, cancel := context.WithTimeout(ctx, d.cfg.Timeout)
			defer cancel()
			cmd := exec.CommandContext(cctx, name, args...)
			cmd.WaitDelay = execWaitDelay
			cmd.Stdin = strings.NewReader(db2SQL)
			return cmd.Output()
		}
	}
	if b, err := queryODBC(ctx, d.cfg.DSN, db2SQL); err == nil {
		if out := parseDB2KV(b); len(out) > 0 {
			return out, nil
		}
	}
	target := firstNonEmpty(d.cfg.Database, d.cfg.DSN)
	b, err := run(ctx, d.cfg.Command, "-x", "-d", target)
	if err != nil {
		return nil, fmt.Errorf("db2: %w", err)
	}
	out := parseDB2KV(b)
	if len(out) == 0 {
		return nil, fmt.Errorf("db2: no metrics")
	}
	return out, nil
}

func parseDB2KV(b []byte) map[string]float64 {
	out := parseSQLKV(b)
	alias := map[string]string{
		"appls_cur_cons": "total", "connections": "total",
		"appls_in_db2": "active", "lock_waits": "waits",
		"lock_timeouts": "timeouts", "lock_escals": "escalations",
		"log_util": "utilization", "log_utilization": "utilization",
		"total_log_used": "log_used", "total_log_available": "log_avail",
		"pool_hits": "hits", "pool_misses": "misses",
	}
	for k, v := range alias {
		if _, ok := out[k]; ok {
			if _, have := out[v]; !have {
				out[v] = out[k]
			}
		}
	}
	return out
}
