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

// dmcacheConfig is collectors.modules.dmcache (dmsetup status --target cache).
type dmcacheConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type dmcacheCollector struct {
	cfg  dmcacheConfig
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	seen map[string]bool
}

func init() {
	Register("dmcache", func() Collector { return &dmcacheCollector{} })
}

func (d *dmcacheCollector) Name() string { return "dmcache" }

func (d *dmcacheCollector) Configure(decode func(v any) error) error {
	if err := decode(&d.cfg); err != nil {
		return err
	}
	if d.cfg.Command == "" {
		d.cfg.Command = "dmsetup"
	}
	if d.cfg.Timeout <= 0 {
		d.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (d *dmcacheCollector) Init(reg *registry.Registry) error {
	if d.cfg.Command == "" {
		if err := d.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	d.seen = map[string]bool{}
	rows, err := d.status(context.Background())
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("dmcache: no cache devices")
	}
	return nil
}

func (d *dmcacheCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	rows, err := d.status(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("dmcache: no cache devices")
	}
	inc := registry.Incremental
	for _, r := range rows {
		d.ensure(reg, r.name, inc)
		id := sanitizeID(r.name)
		_ = reg.Collect("dmcache.device_cache_space."+id, now, map[string]float64{
			"used": r.cacheUsed, "free": r.cacheTotal - r.cacheUsed})
		_ = reg.Collect("dmcache.device_metadata_space."+id, now, map[string]float64{
			"used": r.metaUsed, "free": r.metaTotal - r.metaUsed})
		_ = reg.Collect("dmcache.device_cache_operations."+id, now, map[string]float64{
			"read_hits": r.readHits, "read_misses": r.readMisses, "write_hits": r.writeHits, "write_misses": r.writeMisses})
	}
	return nil
}

func (d *dmcacheCollector) ensure(reg *registry.Registry, name string, inc registry.Algorithm) {
	if d.seen[name] {
		return
	}
	d.seen[name] = true
	id := sanitizeID(name)
	labels := map[string]string{"device": name}
	charts := []*registry.Chart{
		{ID: "dmcache.device_cache_space." + id, Context: "dmcache.device_cache_space", Title: "DM cache space", Units: "blocks", Type: registry.Stacked, Priority: 55700, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "free"}}},
		{ID: "dmcache.device_metadata_space." + id, Context: "dmcache.device_metadata_space", Title: "DM cache metadata space", Units: "blocks", Type: registry.Stacked, Priority: 55710, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "free"}}},
		{ID: "dmcache.device_cache_operations." + id, Context: "dmcache.device_cache_operations", Title: "DM cache operations", Units: "operations/s", Type: registry.Stacked, Priority: 55720, Labels: labels,
			Dimensions: []*registry.Dimension{
				{ID: "read_hits", Algorithm: inc}, {ID: "read_misses", Algorithm: inc},
				{ID: "write_hits", Algorithm: inc}, {ID: "write_misses", Algorithm: inc}}},
	}
	for _, c := range charts {
		c.Family, c.Plugin, c.Module = "dmcache", "dmcache", "dmcache"
		reg.AddChart(c)
	}
}

type dmcacheRow struct {
	name                                         string
	metaUsed, metaTotal, cacheUsed, cacheTotal   float64
	readHits, readMisses, writeHits, writeMisses float64
}

func (d *dmcacheCollector) status(ctx context.Context) ([]dmcacheRow, error) {
	run := d.run
	if run == nil {
		run = execRun(d.cfg.Timeout)
	}
	out, err := run(ctx, d.cfg.Command, "status", "--target", "cache")
	if err != nil {
		return nil, fmt.Errorf("dmsetup: %w", err)
	}
	return parseDMCacheStatus(out)
}

func parseDMCacheStatus(b []byte) ([]dmcacheRow, error) {
	var rows []dmcacheRow
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "No devices") {
			continue
		}
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			name, rest = fields[0], strings.Join(fields[1:], " ")
		}
		f := strings.Fields(rest)
		// start length cache <mdbs> used/total <cbs> used/total read_hits read_misses write_hits write_misses ...
		idx := -1
		for i, x := range f {
			if x == "cache" {
				idx = i
				break
			}
		}
		if idx < 0 || len(f) < idx+7 {
			continue
		}
		meta := strings.Split(f[idx+2], "/")
		cache := strings.Split(f[idx+4], "/")
		if len(meta) != 2 || len(cache) != 2 {
			continue
		}
		r := dmcacheRow{name: strings.TrimSpace(name), metaUsed: firstFloat(meta[0]), metaTotal: firstFloat(meta[1]),
			cacheUsed: firstFloat(cache[0]), cacheTotal: firstFloat(cache[1])}
		if len(f) > idx+8 {
			r.readHits, r.readMisses = firstFloat(f[idx+5]), firstFloat(f[idx+6])
			r.writeHits, r.writeMisses = firstFloat(f[idx+7]), firstFloat(f[idx+8])
		}
		rows = append(rows, r)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("dmcache: no cache devices")
	}
	return rows, nil
}
