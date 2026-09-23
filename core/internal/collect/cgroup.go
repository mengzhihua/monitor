package collect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// cgroupConfig is collectors.modules.cgroup (generic cgroup v2 containers/VMs).
type cgroupConfig struct {
	CgroupRoot string   `yaml:"cgroup_root"`
	Include    []string `yaml:"include"`
	Exclude    []string `yaml:"exclude"`
	Max        int      `yaml:"max"`
}

type cgroupCollector struct {
	cfg     cgroupConfig
	seen    map[string]bool
	cached  []cgroupUnit
	cacheAt time.Time
	scratch []byte
}

func init() {
	Register("cgroup", func() Collector { return &cgroupCollector{} })
}

func (c *cgroupCollector) Name() string { return "cgroup" }

func (c *cgroupCollector) Configure(decode func(v any) error) error {
	if err := decode(&c.cfg); err != nil {
		return err
	}
	if c.cfg.CgroupRoot == "" {
		c.cfg.CgroupRoot = "/sys/fs/cgroup"
	}
	if c.cfg.Max <= 0 {
		c.cfg.Max = 200
	}
	return nil
}

func (c *cgroupCollector) Init(reg *registry.Registry) error {
	if c.cfg.CgroupRoot == "" {
		if err := c.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if runtime.GOOS != "linux" {
		return errors.New("cgroup collector is linux-only")
	}
	if _, err := os.Stat(filepath.Join(c.cfg.CgroupRoot, "cgroup.controllers")); err != nil {
		return fmt.Errorf("cgroup v2 unified hierarchy not found at %s", c.cfg.CgroupRoot)
	}
	if len(c.scan()) == 0 {
		return fmt.Errorf("cgroup: no container/VM cgroups under %s", c.cfg.CgroupRoot)
	}
	c.seen = map[string]bool{}
	return nil
}

func (c *cgroupCollector) Collect(_ context.Context, reg *registry.Registry, now time.Time) error {
	groups := c.cachedScan(now)
	if len(groups) == 0 {
		return fmt.Errorf("cgroup: no container/VM cgroups")
	}
	for _, g := range groups {
		c.ensure(reg, g)
		id := g.id
		var cpuVals [3]float64
		if b, err := readInto(filepath.Join(g.dir, "cpu.stat"), &c.scratch); err == nil {
			cpuVals[0], _ = kvKey(b, "user_usec")
			cpuVals[1], _ = kvKey(b, "system_usec")
			cpuVals[2], _ = kvKey(b, "usage_usec")
		}
		_ = reg.Collect("cgroup_"+id+".cpu", now, map[string]float64{
			"user": cpuVals[0], "system": cpuVals[1], "usage": cpuVals[2]})
		mem := map[string]float64{}
		if v, err := readUintBuf(filepath.Join(g.dir, "memory.current"), &c.scratch); err == nil {
			mem["ram"] = v
		}
		if b, err := readInto(filepath.Join(g.dir, "memory.stat"), &c.scratch); err == nil {
			if v, ok := kvKey(b, "inactive_file"); ok && v > 0 {
				if mem["ram"] > v {
					mem["ram"] -= v
				}
				mem["cache"] = v
			}
		}
		_ = reg.Collect("cgroup_"+id+".mem", now, mem)
		rd, wr, ok := readIOStatBuf(filepath.Join(g.dir, "io.stat"), &c.scratch)
		if ok {
			_ = reg.Collect("cgroup_"+id+".throttle_io", now, map[string]float64{"read": rd, "write": wr})
		}
		if v, err := readUintBuf(filepath.Join(g.dir, "pids.current"), &c.scratch); err == nil {
			_ = reg.Collect("cgroup_"+id+".pids_current", now, map[string]float64{"pids": v})
		}
	}
	return nil
}

type cgroupUnit struct {
	id, name, dir, kind string
}

func (c *cgroupCollector) ensure(reg *registry.Registry, g cgroupUnit) {
	if c.seen[g.id] {
		return
	}
	c.seen[g.id] = true
	inc := registry.Incremental
	prefix := "cgroup_" + g.id
	labels := map[string]string{"cgroup": g.name, "kind": g.kind}
	cpu := &registry.Chart{ID: prefix + ".cpu", Context: "cgroup.cpu", Title: "CPU Usage", Units: "percentage",
		Type: registry.Stacked, Priority: 40000, Labels: labels, Dimensions: []*registry.Dimension{
			{ID: "user", Algorithm: inc, Divisor: 10_000},
			{ID: "system", Algorithm: inc, Divisor: 10_000},
			{ID: "usage", Algorithm: inc, Divisor: 10_000}}}
	mem := &registry.Chart{ID: prefix + ".mem", Context: "cgroup.mem", Title: "Memory Usage", Units: "MiB",
		Type: registry.Stacked, Priority: 40010, Labels: labels, Dimensions: []*registry.Dimension{
			{ID: "ram", Divisor: 1 << 20}, {ID: "cache", Divisor: 1 << 20}}}
	io := &registry.Chart{ID: prefix + ".throttle_io", Context: "cgroup.throttle_io", Title: "I/O Bandwidth", Units: "KiB/s",
		Type: registry.Area, Priority: 40020, Labels: labels, Dimensions: []*registry.Dimension{
			{ID: "read", Algorithm: inc, Divisor: 1024}, {ID: "write", Algorithm: inc, Multiplier: -1, Divisor: 1024}}}
	pids := &registry.Chart{ID: prefix + ".pids_current", Context: "cgroup.pids_current", Title: "PIDs", Units: "pids",
		Priority: 40030, Labels: labels, Dimensions: []*registry.Dimension{{ID: "pids"}}}
	for _, ch := range []*registry.Chart{cpu, mem, io, pids} {
		ch.Family, ch.Plugin, ch.Module = "cgroup", "cgroup", "cgroup"
		reg.AddChart(ch)
	}
}

func (c *cgroupCollector) cachedScan(now time.Time) []cgroupUnit {
	if len(c.cached) > 0 && !c.cacheAt.IsZero() && now.Sub(c.cacheAt) < cgroupWalkEvery {
		return c.cached
	}
	c.cached = c.scan()
	c.cacheAt = now
	return c.cached
}

func (c *cgroupCollector) scan() []cgroupUnit {
	var out []cgroupUnit
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > 8 {
			return
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			n := e.Name()
			path := filepath.Join(dir, n)
			if kind, ok := cgroupKind(n); ok && c.wanted(n) {
				id := sanitizeID(cgroupShortName(n))
				out = append(out, cgroupUnit{id: id, name: cgroupShortName(n), dir: path, kind: kind})
			}
			if strings.HasSuffix(n, ".slice") || n == "docker" || n == "lxc" || n == "machine.slice" ||
				strings.Contains(n, "kubepods") || strings.Contains(n, "containerd") || strings.HasSuffix(n, ".service") {
				walk(path, depth+1)
			}
		}
	}
	walk(c.cfg.CgroupRoot, 0)
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	if len(out) > c.cfg.Max {
		out = out[:c.cfg.Max]
	}
	return out
}

func (c *cgroupCollector) wanted(name string) bool {
	short := cgroupShortName(name)
	for _, p := range c.cfg.Exclude {
		if globMatch(p, name) || globMatch(p, short) {
			return false
		}
	}
	if len(c.cfg.Include) == 0 {
		return true
	}
	for _, p := range c.cfg.Include {
		if globMatch(p, name) || globMatch(p, short) {
			return true
		}
	}
	return false
}

func cgroupKind(name string) (string, bool) {
	n := strings.ToLower(name)
	switch {
	case strings.HasSuffix(n, ".service"), strings.HasSuffix(n, ".mount"),
		strings.HasSuffix(n, ".socket"), strings.HasSuffix(n, ".swap"),
		n == "init.scope":
		return "", false
	case strings.HasPrefix(n, "docker-") && strings.HasSuffix(n, ".scope"):
		return "docker", true
	case strings.Contains(n, "containerd") && strings.HasSuffix(n, ".scope"):
		return "containerd", true
	case strings.HasPrefix(n, "libpod-") || strings.HasPrefix(n, "podman-"):
		return "podman", true
	case strings.HasPrefix(n, "crio-"):
		return "crio", true
	case strings.HasPrefix(n, "lxc") && n != "lxc":
		return "lxc", true
	case strings.HasPrefix(n, "machine-qemu") || strings.HasPrefix(n, "machine-"):
		return "qemu", true
	case strings.Contains(n, "kubepods") && strings.Contains(n, "pod"):
		return "k8s", true
	case len(n) == 64 && cgroupIsHex(n):
		return "docker", true
	}
	return "", false
}

func cgroupShortName(name string) string {
	n := name
	n = strings.TrimSuffix(n, ".scope")
	for _, p := range []string{"docker-", "cri-containerd-", "containerd-", "libpod-", "podman-", "crio-", "lxc-", "machine-"} {
		n = strings.TrimPrefix(n, p)
	}
	if i := strings.Index(n, "pod"); i >= 0 && strings.Contains(strings.ToLower(name), "kubepods") {
		n = n[i:]
	}
	if len(n) > 12 && cgroupIsHex(n) {
		n = n[:12]
	}
	return n
}

func cgroupIsHex(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return s != ""
}
