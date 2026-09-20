package collect

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// systemdConfig is the `collectors.modules.systemd` section. Metrics come
// straight from the unified cgroup v2 hierarchy, no D-Bus needed.
type systemdConfig struct {
	CgroupRoot string   `yaml:"cgroup_root"` // default /sys/fs/cgroup
	Slices     []string `yaml:"slices"`      // default [system.slice]
	Include    []string `yaml:"include"`     // globs on unit name; empty = all
	Exclude    []string `yaml:"exclude"`
	Max        int      `yaml:"max"` // cap on tracked services (default 200)
}

type systemdCollector struct {
	cfg   systemdConfig
	hasIO bool
	units map[string]bool
}

func init() {
	Register("systemd", func() Collector { return &systemdCollector{} })
}

func (s *systemdCollector) Name() string { return "systemd" }

func (s *systemdCollector) Configure(decode func(v any) error) error {
	if err := decode(&s.cfg); err != nil {
		return err
	}
	if s.cfg.CgroupRoot == "" {
		s.cfg.CgroupRoot = "/sys/fs/cgroup"
	}
	if len(s.cfg.Slices) == 0 {
		s.cfg.Slices = []string{"system.slice"}
	}
	if s.cfg.Max <= 0 {
		s.cfg.Max = 200
	}
	return nil
}

func (s *systemdCollector) Init(reg *registry.Registry) error {
	if s.cfg.CgroupRoot == "" {
		if err := s.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if runtime.GOOS != "linux" {
		return errors.New("systemd collector is linux-only")
	}
	if _, err := os.Stat(filepath.Join(s.cfg.CgroupRoot, "cgroup.controllers")); err != nil {
		return fmt.Errorf("cgroup v2 unified hierarchy not found at %s", s.cfg.CgroupRoot)
	}
	units := s.scan()
	if len(units) == 0 {
		return fmt.Errorf("no *.service cgroups under %s", s.cfg.CgroupRoot)
	}
	s.units = map[string]bool{}
	for _, u := range units {
		if _, err := os.Stat(filepath.Join(u.dir, "io.stat")); err == nil {
			s.hasIO = true
			break
		}
	}
	reg.AddChart(&registry.Chart{ID: "systemd.cpu", Family: "systemd", Title: "systemd services CPU utilization", Units: "percentage",
		Type: registry.Stacked, Priority: 20000, Plugin: "systemd", Module: "systemd"})
	reg.AddChart(&registry.Chart{ID: "systemd.mem", Family: "systemd", Title: "systemd services memory", Units: "MiB",
		Type: registry.Stacked, Priority: 20010, Plugin: "systemd", Module: "systemd"})
	if s.hasIO {
		reg.AddChart(&registry.Chart{ID: "systemd.io_read", Family: "systemd", Title: "systemd services disk reads", Units: "KiB/s",
			Type: registry.Stacked, Priority: 20020, Plugin: "systemd", Module: "systemd"})
		reg.AddChart(&registry.Chart{ID: "systemd.io_write", Family: "systemd", Title: "systemd services disk writes", Units: "KiB/s",
			Type: registry.Stacked, Priority: 20030, Plugin: "systemd", Module: "systemd"})
	}
	return nil
}

type sdUnit struct {
	name, dir string
}

// scan lists *.service cgroups under the configured slices (one level of
// nesting is enough for system.slice; slices inside slices are walked too).
func (s *systemdCollector) scan() []sdUnit {
	var out []sdUnit
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			n := e.Name()
			switch {
			case strings.HasSuffix(n, ".service"):
				if s.wanted(strings.TrimSuffix(n, ".service")) {
					out = append(out, sdUnit{name: strings.TrimSuffix(n, ".service"), dir: filepath.Join(dir, n)})
				}
			case strings.HasSuffix(n, ".slice") && depth < 3:
				walk(filepath.Join(dir, n), depth+1)
			}
		}
	}
	for _, sl := range s.cfg.Slices {
		walk(filepath.Join(s.cfg.CgroupRoot, sl), 0)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	if len(out) > s.cfg.Max {
		out = out[:s.cfg.Max]
	}
	return out
}

func (s *systemdCollector) wanted(name string) bool {
	for _, p := range s.cfg.Exclude {
		if globMatch(p, name) {
			return false
		}
	}
	if len(s.cfg.Include) == 0 {
		return true
	}
	for _, p := range s.cfg.Include {
		if globMatch(p, name) {
			return true
		}
	}
	return false
}

func (s *systemdCollector) Collect(_ context.Context, reg *registry.Registry, now time.Time) error {
	cpu := map[string]float64{}
	mem := map[string]float64{}
	rd := map[string]float64{}
	wr := map[string]float64{}
	for _, u := range s.scan() {
		dimID := sanitizeID(u.name)
		if !s.units[u.name] {
			s.units[u.name] = true
			addDim := func(chart string, d *registry.Dimension) {
				if c, ok := reg.Chart(chart); ok {
					c.AddDimension(d)
				}
			}
			addDim("systemd.cpu", &registry.Dimension{ID: dimID, Name: u.name, Algorithm: registry.Incremental, Divisor: 10_000}) // usec/s → %
			addDim("systemd.mem", &registry.Dimension{ID: dimID, Name: u.name, Divisor: 1 << 20})
			if s.hasIO {
				addDim("systemd.io_read", &registry.Dimension{ID: dimID, Name: u.name, Algorithm: registry.Incremental, Divisor: 1024})
				addDim("systemd.io_write", &registry.Dimension{ID: dimID, Name: u.name, Algorithm: registry.Incremental, Divisor: 1024})
			}
		}
		if v, ok := readKV(filepath.Join(u.dir, "cpu.stat"))["usage_usec"]; ok {
			cpu[dimID] = v
		}
		if v, err := readUint(filepath.Join(u.dir, "memory.current")); err == nil {
			mem[dimID] = v
		}
		if s.hasIO {
			r, w, ok := readIOStat(filepath.Join(u.dir, "io.stat"))
			if ok {
				rd[dimID], wr[dimID] = r, w
			}
		}
	}
	_ = reg.Collect("systemd.cpu", now, cpu)
	_ = reg.Collect("systemd.mem", now, mem)
	if s.hasIO {
		_ = reg.Collect("systemd.io_read", now, rd)
		_ = reg.Collect("systemd.io_write", now, wr)
	}
	return nil
}

// readKV parses "key value" lines (cpu.stat, memory.stat).
func readKV(path string) map[string]float64 {
	out := map[string]float64{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if !ok {
			continue
		}
		if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			out[k] = n
		}
	}
	return out
}

func readUint(path string) (float64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(b))
	if s == "max" {
		return 0, errors.New("max")
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return float64(n), err
}

// readIOStat sums rbytes/wbytes across devices in a cgroup v2 io.stat file.
func readIOStat(path string) (rd, wr float64, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		for _, fld := range strings.Fields(sc.Text())[1:] {
			k, v, found := strings.Cut(fld, "=")
			if !found {
				continue
			}
			n, err := strconv.ParseFloat(v, 64)
			if err != nil {
				continue
			}
			switch k {
			case "rbytes":
				rd += n
			case "wbytes":
				wr += n
			}
		}
		ok = true
	}
	return rd, wr, ok
}
