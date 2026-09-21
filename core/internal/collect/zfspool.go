package collect

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// zfspoolConfig is collectors.modules.zfspool (zpool list).
type zfspoolConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type zfspoolCollector struct {
	cfg  zfspoolConfig
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	seen map[string]bool
}

func init() {
	Register("zfspool", func() Collector { return &zfspoolCollector{} })
}

func (z *zfspoolCollector) Name() string { return "zfspool" }

func (z *zfspoolCollector) Configure(decode func(v any) error) error {
	if err := decode(&z.cfg); err != nil {
		return err
	}
	if z.cfg.Command == "" {
		z.cfg.Command = "zpool"
	}
	if z.cfg.Timeout <= 0 {
		z.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (z *zfspoolCollector) Init(reg *registry.Registry) error {
	if z.cfg.Command == "" {
		if err := z.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	z.seen = map[string]bool{}
	pools, err := z.list(context.Background())
	if err != nil {
		return err
	}
	if len(pools) == 0 {
		return fmt.Errorf("zfspool: no pools")
	}
	return nil
}

func (z *zfspoolCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	pools, err := z.list(ctx)
	if err != nil {
		return err
	}
	if len(pools) == 0 {
		return fmt.Errorf("zfspool: no pools")
	}
	for _, p := range pools {
		z.ensure(reg, p.name)
		id := sanitizeID(p.name)
		st := map[string]float64{"online": 0, "degraded": 0, "faulted": 0, "offline": 0, "unavail": 0, "removed": 0, "suspended": 0}
		key := strings.ToLower(p.health)
		if _, ok := st[key]; !ok {
			key = "faulted"
		}
		st[key] = 1
		_ = reg.Collect("zfspool.pool_health_state."+id, now, st)
		_ = reg.Collect("zfspool.pool_space_utilization."+id, now, map[string]float64{"utilization": p.cap})
		_ = reg.Collect("zfspool.pool_space_usage."+id, now, map[string]float64{"free": p.free, "used": p.alloc})
		_ = reg.Collect("zfspool.pool_fragmentation."+id, now, map[string]float64{"fragmentation": p.frag})
	}
	return nil
}

func (z *zfspoolCollector) ensure(reg *registry.Registry, name string) {
	if z.seen[name] {
		return
	}
	z.seen[name] = true
	id := sanitizeID(name)
	labels := map[string]string{"pool": name}
	charts := []*registry.Chart{
		{ID: "zfspool.pool_health_state." + id, Context: "zfspool.pool_health_state", Title: "Zpool health state", Units: "state", Priority: 55600, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "online"}, {ID: "degraded"}, {ID: "faulted"}, {ID: "offline"}, {ID: "unavail"}, {ID: "removed"}, {ID: "suspended"}}},
		{ID: "zfspool.pool_space_utilization." + id, Context: "zfspool.pool_space_utilization", Title: "Zpool space utilization", Units: "percentage", Type: registry.Area, Priority: 55610, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "utilization"}}},
		{ID: "zfspool.pool_space_usage." + id, Context: "zfspool.pool_space_usage", Title: "Zpool space usage", Units: "bytes", Type: registry.Stacked, Priority: 55620, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "free"}, {ID: "used"}}},
		{ID: "zfspool.pool_fragmentation." + id, Context: "zfspool.pool_fragmentation", Title: "Zpool fragmentation", Units: "percentage", Priority: 55630, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "fragmentation"}}},
	}
	for _, c := range charts {
		c.Family, c.Plugin, c.Module = "zfspool", "zfspool", "zfspool"
		reg.AddChart(c)
	}
}

type zpoolRow struct {
	name, health                 string
	size, alloc, free, frag, cap float64
}

func (z *zfspoolCollector) list(ctx context.Context) ([]zpoolRow, error) {
	run := z.run
	if run == nil {
		run = execRun(z.cfg.Timeout)
	}
	out, err := run(ctx, z.cfg.Command, "list", "-Hp", "-o", "name,size,alloc,free,frag,cap,health")
	if err != nil {
		return nil, fmt.Errorf("zpool: %w", err)
	}
	return parseZpoolList(out)
}

func parseZpoolList(b []byte) ([]zpoolRow, error) {
	var rows []zpoolRow
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 7 {
			continue
		}
		rows = append(rows, zpoolRow{
			name: f[0], size: firstFloat(f[1]), alloc: firstFloat(f[2]), free: firstFloat(f[3]),
			frag: firstFloat(f[4]), cap: firstFloat(f[5]), health: f[6],
		})
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("zfspool: empty pool list")
	}
	return rows, nil
}
