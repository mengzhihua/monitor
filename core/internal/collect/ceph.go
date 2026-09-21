package collect

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// cephConfig is collectors.modules.ceph (`ceph status --format json`).
type cephConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type cephCollector struct {
	cfg cephConfig
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func init() {
	Register("ceph", func() Collector { return &cephCollector{} })
}

func (c *cephCollector) Name() string { return "ceph" }

func (c *cephCollector) Configure(decode func(v any) error) error {
	if err := decode(&c.cfg); err != nil {
		return err
	}
	if c.cfg.Command == "" {
		c.cfg.Command = "ceph"
	}
	if c.cfg.Timeout <= 0 {
		c.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (c *cephCollector) Init(reg *registry.Registry) error {
	if c.cfg.Command == "" {
		if err := c.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if c.run == nil {
		c.run = execRun(c.cfg.Timeout)
	}
	if _, err := c.status(context.Background()); err != nil {
		return err
	}
	for _, ch := range []*registry.Chart{
		{ID: "ceph.cluster_status", Title: "Ceph Cluster Status", Units: "status", Priority: 56300,
			Dimensions: []*registry.Dimension{{ID: "ok"}, {ID: "warn"}, {ID: "err"}}},
		{ID: "ceph.cluster_osds_by_status_count", Title: "Ceph Cluster OSDs by Status", Units: "osds", Priority: 56310,
			Dimensions: []*registry.Dimension{{ID: "up"}, {ID: "down"}, {ID: "in"}, {ID: "out"}}},
		{ID: "ceph.cluster_physical_capacity_usage", Title: "Ceph Cluster Physical Capacity Usage", Units: "bytes", Type: registry.Stacked, Priority: 56320,
			Dimensions: []*registry.Dimension{{ID: "avail"}, {ID: "used"}}},
		{ID: "ceph.cluster_pgs_count", Title: "Ceph Cluster Placement Groups", Units: "pgs", Priority: 56330,
			Dimensions: []*registry.Dimension{{ID: "pgs"}}},
		{ID: "ceph.cluster_client_io", Title: "Ceph Cluster Client IO", Units: "bytes/s", Type: registry.Area, Priority: 56340,
			Dimensions: []*registry.Dimension{{ID: "read"}, {ID: "written", Multiplier: -1}}},
		{ID: "ceph.cluster_client_iops", Title: "Ceph Cluster Client IOPS", Units: "ops/s", Priority: 56350,
			Dimensions: []*registry.Dimension{{ID: "read"}, {ID: "write", Multiplier: -1}}},
		{ID: "ceph.cluster_monitors_count", Title: "Ceph Cluster Monitors", Units: "monitors", Priority: 56360,
			Dimensions: []*registry.Dimension{{ID: "monitors"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "ceph", "ceph", "ceph"
		reg.AddChart(ch)
	}
	return nil
}

func (c *cephCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := c.status(ctx)
	if err != nil {
		return err
	}
	st := strings.ToUpper(nestString(m, "health", "status"))
	ok, warn, errn := 0.0, 0.0, 0.0
	switch {
	case strings.Contains(st, "ERR"):
		errn = 1
	case strings.Contains(st, "WARN"):
		warn = 1
	default:
		ok = 1
	}
	_ = reg.Collect("ceph.cluster_status", now, map[string]float64{"ok": ok, "warn": warn, "err": errn})
	osd := nestMap(m, "osdmap")
	if osd == nil {
		osd = nestMap(m, "osdmap", "osdmap")
	}
	num := nestFloat(osd, "num_osds")
	up := nestFloat(osd, "num_up_osds")
	in := nestFloat(osd, "num_in_osds")
	_ = reg.Collect("ceph.cluster_osds_by_status_count", now, map[string]float64{
		"up": up, "down": num - up, "in": in, "out": num - in,
	})
	pg := nestMap(m, "pgmap")
	_ = reg.Collect("ceph.cluster_physical_capacity_usage", now, map[string]float64{
		"avail": nestFloat(pg, "bytes_avail"), "used": nestFloat(pg, "bytes_used"),
	})
	_ = reg.Collect("ceph.cluster_pgs_count", now, map[string]float64{"pgs": nestFloat(pg, "num_pgs")})
	_ = reg.Collect("ceph.cluster_client_io", now, map[string]float64{
		"read": nestFloat(pg, "read_bytes_sec"), "written": nestFloat(pg, "write_bytes_sec"),
	})
	_ = reg.Collect("ceph.cluster_client_iops", now, map[string]float64{
		"read": nestFloat(pg, "read_op_per_sec"), "write": nestFloat(pg, "write_op_per_sec"),
	})
	mons := nestFloat(m, "monmap", "num_mons")
	if mons == 0 {
		mons = float64(len(nestSlice(m, "monmap", "mons")))
	}
	_ = reg.Collect("ceph.cluster_monitors_count", now, map[string]float64{"monitors": mons})
	return nil
}

func (c *cephCollector) status(ctx context.Context) (map[string]any, error) {
	b, err := c.run(ctx, c.cfg.Command, "status", "--format", "json")
	if err != nil {
		b, err = c.run(ctx, c.cfg.Command, "-f", "json", "status")
		if err != nil {
			return nil, fmt.Errorf("ceph: %w", err)
		}
	}
	m, err := jsonMap(b)
	if err != nil {
		return nil, fmt.Errorf("ceph: %w", err)
	}
	if nestMap(m, "health") == nil && nestMap(m, "osdmap") == nil {
		return nil, fmt.Errorf("ceph: no status")
	}
	return m, nil
}
