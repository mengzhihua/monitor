package collect

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	Max        int      `yaml:"max"`     // cap on tracked services (default 200)
	Command    string   `yaml:"command"` // systemctl; missing command skips unit-state charts
}

type systemdCollector struct {
	cfg          systemdConfig
	hasIO        bool
	hasCgroup    bool
	hasUnits     bool
	units        map[string]bool
	run          func(ctx context.Context, name string, args ...string) ([]byte, error)
	scanned      []sdUnit
	scanAt       time.Time
	unitAt       time.Time
	unitCounts   map[string]float64
	unitRestarts float64
}

// cgroupWalkEvery is how often the cgroup tree is listed. Per-unit counters
// are still read every tick; only the directory walk is cached.
const cgroupWalkEvery = 10 * time.Second

// unitPollEvery is how often systemctl is spawned. Unit state is a gauge.
const unitPollEvery = 10 * time.Second

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
	if s.cfg.Command == "" {
		s.cfg.Command = "systemctl"
	}
	return nil
}

func (s *systemdCollector) Init(reg *registry.Registry) error {
	if s.cfg.CgroupRoot == "" {
		if err := s.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if runtime.GOOS != "linux" && s.run == nil {
		return errors.New("systemd collector is linux-only")
	}
	if _, err := os.Stat(filepath.Join(s.cfg.CgroupRoot, "cgroup.controllers")); err == nil {
		units := s.scan()
		if len(units) > 0 {
			s.hasCgroup = true
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
		}
	}
	if rows, err := s.unitRows(context.Background()); err == nil && len(rows) > 0 {
		s.hasUnits = true
		reg.AddChart(&registry.Chart{ID: "systemd.service_units", Family: "systemd", Title: "systemd service units by state",
			Units: "units", Type: registry.Stacked, Priority: 20040, Plugin: "systemd", Module: "systemd",
			Dimensions: []*registry.Dimension{
				{ID: "active"}, {ID: "inactive"}, {ID: "activating"}, {ID: "deactivating"}, {ID: "failed"}, {ID: "other"},
			}})
		reg.AddChart(&registry.Chart{ID: "systemd.service_restarts", Family: "systemd", Title: "systemd service restarts",
			Units: "restarts", Priority: 20041, Plugin: "systemd", Module: "systemd",
			Dimensions: []*registry.Dimension{{ID: "restarts"}}})
	}
	if !s.hasCgroup && !s.hasUnits {
		return fmt.Errorf("no systemd cgroups or systemctl units")
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

func (s *systemdCollector) cachedUnits(now time.Time) []sdUnit {
	if len(s.scanned) > 0 && !s.scanAt.IsZero() && now.Sub(s.scanAt) < cgroupWalkEvery {
		return s.scanned
	}
	s.scanned = s.scan()
	s.scanAt = now
	return s.scanned
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

func (s *systemdCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	if s.hasCgroup {
		s.collectCgroup(reg, now)
	}
	if s.hasUnits {
		s.collectUnits(ctx, reg, now)
	}
	return nil
}

func (s *systemdCollector) collectCgroup(reg *registry.Registry, now time.Time) {
	cpu := map[string]float64{}
	mem := map[string]float64{}
	rd := map[string]float64{}
	wr := map[string]float64{}
	for _, u := range s.cachedUnits(now) {
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
}

func (s *systemdCollector) collectUnits(ctx context.Context, reg *registry.Registry, now time.Time) {
	if s.unitAt.IsZero() || now.Sub(s.unitAt) >= unitPollEvery {
		rows, err := s.unitRows(ctx)
		if err != nil {
			if s.unitCounts == nil {
				return
			}
		} else {
			counts := map[string]float64{"active": 0, "inactive": 0, "activating": 0, "deactivating": 0, "failed": 0, "other": 0}
			var restarts float64
			for _, r := range rows {
				st := strings.ToLower(r.ActiveState)
				if _, ok := counts[st]; ok && st != "other" {
					counts[st]++
				} else {
					counts["other"]++
				}
				restarts += float64(r.NRestarts)
			}
			s.unitCounts = counts
			s.unitRestarts = restarts
			s.unitAt = now
		}
	}
	if s.unitCounts == nil {
		return
	}
	_ = reg.Collect("systemd.service_units", now, s.unitCounts)
	_ = reg.Collect("systemd.service_restarts", now, map[string]float64{"restarts": s.unitRestarts})
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

func (s *systemdCollector) Functions() []Function {
	return []Function{{
		Name:    "services",
		Help:    "Linux systemd services (state, restarts, cgroup cpu/memory)",
		Timeout: 10,
		Run: func(_ context.Context, args map[string]string) (any, error) {
			return s.services(args), nil
		},
	}}
}

type ServiceRow struct {
	Name          string  `json:"name"`
	ActiveState   string  `json:"active_state"`
	SubState      string  `json:"sub_state"`
	Result        string  `json:"result"`
	NRestarts     int     `json:"nrestarts"`
	UnitFileState string  `json:"unit_file_state"`
	CPU           float64 `json:"cpu_usec"` // cumulative
	Memory        uint64  `json:"memory"`   // bytes
}

func (s *systemdCollector) services(args map[string]string) Table {
	byName := map[string]*ServiceRow{}
	var order []string
	add := func(name string) *ServiceRow {
		if r, ok := byName[name]; ok {
			return r
		}
		r := &ServiceRow{Name: name}
		byName[name] = r
		order = append(order, name)
		return r
	}
	if s.hasCgroup || s.cfg.CgroupRoot != "" {
		for _, u := range s.scan() {
			row := add(u.name)
			if m, err := readUint(filepath.Join(u.dir, "memory.current")); err == nil {
				row.Memory = uint64(m)
			}
			if st := readKV(filepath.Join(u.dir, "cpu.stat")); st != nil {
				row.CPU = st["usage_usec"]
			}
		}
	}
	if rows, err := s.unitRows(context.Background()); err == nil {
		for _, u := range rows {
			row := add(u.Name)
			row.ActiveState = u.ActiveState
			row.SubState = u.SubState
			row.Result = u.Result
			row.NRestarts = u.NRestarts
			row.UnitFileState = u.UnitFileState
		}
	}
	rows := make([]ServiceRow, 0, len(order))
	for _, name := range order {
		rows = append(rows, *byName[name])
	}
	sortBy := args["sort"]
	sort.Slice(rows, func(i, j int) bool {
		switch sortBy {
		case "name":
			return rows[i].Name < rows[j].Name
		case "cpu":
			return rows[i].CPU > rows[j].CPU
		case "restarts":
			return rows[i].NRestarts > rows[j].NRestarts
		default:
			if rows[i].Memory != rows[j].Memory {
				return rows[i].Memory > rows[j].Memory
			}
			return rows[i].Name < rows[j].Name
		}
	})
	out := Table{Columns: []string{"name", "active_state", "sub_state", "result", "nrestarts", "unit_file_state", "cpu_usec", "memory"}, Total: len(rows)}
	out.Rows = make([]any, len(rows))
	for i, r := range rows {
		out.Rows[i] = r
	}
	return out
}

func (s *systemdCollector) unitRows(ctx context.Context) ([]ServiceRow, error) {
	cmd := s.cfg.Command
	if cmd == "" {
		cmd = "systemctl"
	}
	out, err := s.exec(ctx, cmd, "show", "--type=service", "--all", "--no-pager",
		"--property=Id,ActiveState,SubState,Result,NRestarts,UnitFileState")
	if err != nil {
		return nil, err
	}
	return parseSystemctlShow(out), nil
}

func (s *systemdCollector) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	if s.run != nil {
		return s.run(ctx, name, args...)
	}
	if _, err := exec.LookPath(name); err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return exec.CommandContext(cctx, name, args...).Output()
}

func parseSystemctlShow(b []byte) []ServiceRow {
	var rows []ServiceRow
	var cur ServiceRow
	flush := func() {
		if cur.Name == "" && cur.ActiveState == "" {
			return
		}
		cur.Name = strings.TrimSuffix(cur.Name, ".service")
		rows = append(rows, cur)
		cur = ServiceRow{}
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			flush()
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "Id":
			cur.Name = v
		case "ActiveState":
			cur.ActiveState = v
		case "SubState":
			cur.SubState = v
		case "Result":
			cur.Result = v
		case "NRestarts":
			cur.NRestarts, _ = strconv.Atoi(v)
		case "UnitFileState":
			cur.UnitFileState = v
		}
	}
	flush()
	return rows
}
