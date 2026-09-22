package collect

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func init() {
	Register("cpu", func() Collector { return &cpuCollector{} })
	Register("load", func() Collector { return &loadCollector{} })
	Register("mem", func() Collector { return &memCollector{} })
	Register("disk", func() Collector { return &diskCollector{} })
	Register("diskspace", func() Collector { return &diskSpaceCollector{} })
	Register("net", func() Collector { return &netCollector{} })
	Register("uptime", func() Collector { return &uptimeCollector{} })
}

// ---- cpu ----

type cpuCollector struct{ ncpu int }

func (c *cpuCollector) Name() string { return "cpu" }

var cpuDims = []string{"guest_nice", "guest", "steal", "softirq", "irq", "user", "system", "nice", "iowait"}

func (c *cpuCollector) Init(reg *registry.Registry) error {
	if _, err := cpu.Times(false); err != nil {
		return err
	}
	c.ncpu, _ = cpu.Counts(true)
	ch := &registry.Chart{ID: "system.cpu", Family: "cpu", Title: "Total CPU utilization", Units: "percentage",
		Type: registry.Stacked, Priority: 100, Plugin: "system", Module: "cpu"}
	for _, d := range cpuDims {
		ch.Dimensions = append(ch.Dimensions, &registry.Dimension{ID: d, Algorithm: registry.PercentageOfIncrementalRow})
	}
	ch.Dimensions = append(ch.Dimensions, &registry.Dimension{ID: "idle", Algorithm: registry.PercentageOfIncrementalRow, Hidden: true})
	reg.AddChart(ch)

	per, err := cpu.Times(true)
	if err == nil && len(per) > 1 {
		for i := range per {
			pc := &registry.Chart{ID: fmt.Sprintf("cpu.cpu%d", i), Context: "cpu.cpu", Family: "utilization",
				Title: fmt.Sprintf("Core %d utilization", i), Units: "percentage", Type: registry.Stacked, Priority: 1000 + i,
				Plugin: "system", Module: "cpu", Labels: map[string]string{"cpu": fmt.Sprint(i)}}
			for _, d := range cpuDims {
				pc.Dimensions = append(pc.Dimensions, &registry.Dimension{ID: d, Algorithm: registry.PercentageOfIncrementalRow})
			}
			pc.Dimensions = append(pc.Dimensions, &registry.Dimension{ID: "idle", Algorithm: registry.PercentageOfIncrementalRow, Hidden: true})
			reg.AddChart(pc)
		}
	}
	return nil
}

func cpuRaw(t cpu.TimesStat) map[string]float64 {
	user, nice := t.User, t.Nice
	if runtime.GOOS == "linux" {
		// /proc/stat already folds guest time into user and nice.
		user = max(user-t.Guest, 0)
		nice = max(nice-t.GuestNice, 0)
	}
	return map[string]float64{
		"user": user, "system": t.System, "nice": nice, "iowait": t.Iowait, "irq": t.Irq,
		"softirq": t.Softirq, "steal": t.Steal, "guest": t.Guest, "guest_nice": t.GuestNice, "idle": t.Idle,
	}
}

func (c *cpuCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	total, err := cpu.TimesWithContext(ctx, false)
	if err != nil || len(total) == 0 {
		return fmt.Errorf("cpu times: %w", err)
	}
	if err := reg.Collect("system.cpu", now, cpuRaw(total[0])); err != nil {
		return err
	}
	per, err := cpu.TimesWithContext(ctx, true)
	if err == nil && len(per) > 1 {
		for i, t := range per {
			id := fmt.Sprintf("cpu.cpu%d", i)
			if _, ok := reg.Chart(id); ok {
				_ = reg.Collect(id, now, cpuRaw(t))
			}
		}
	}
	return nil
}

// ---- load ----

type loadCollector struct {
	lastLoad time.Time
	every    time.Duration
}

// loadEvery is how often load averages are sampled: the kernel only refreshes
// them every 5 seconds, and a chart cannot tick faster than the scheduler.
func loadEvery(reg *registry.Registry) int {
	return max(5, reg.Host.UpdateEvery)
}

func (c *loadCollector) Name() string { return "load" }

func (c *loadCollector) Init(reg *registry.Registry) error {
	if _, err := load.Avg(); err != nil {
		return err
	}
	c.every = time.Duration(loadEvery(reg)) * time.Second
	reg.AddChart(&registry.Chart{ID: "system.load", Family: "load", Title: "System load average", Units: "load",
		Priority: 200, Plugin: "system", Module: "load", UpdateEvery: loadEvery(reg),
		Dimensions: []*registry.Dimension{{ID: "load1"}, {ID: "load5"}, {ID: "load15"}}})
	if m, err := load.Misc(); err == nil && m != nil && (m.ProcsTotal > 0 || m.ProcsRunning > 0) {
		reg.AddChart(&registry.Chart{ID: "system.processes", Family: "processes", Title: "System processes", Units: "processes",
			Priority: 210, Plugin: "system", Module: "load",
			Dimensions: []*registry.Dimension{{ID: "running"}, {ID: "blocked"}, {ID: "total"}}})
		reg.AddChart(&registry.Chart{ID: "system.ctxt", Family: "processes", Title: "CPU context switches", Units: "switches/s",
			Priority: 211, Plugin: "system", Module: "load",
			Dimensions: []*registry.Dimension{{ID: "switches", Algorithm: registry.Incremental}}})
	}
	return nil
}

func (c *loadCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	if now.Sub(c.lastLoad) >= c.every {
		a, err := load.AvgWithContext(ctx)
		if err != nil {
			return err
		}
		c.lastLoad = now
		_ = reg.Collect("system.load", now, map[string]float64{"load1": a.Load1, "load5": a.Load5, "load15": a.Load15})
	}
	if _, ok := reg.Chart("system.processes"); ok {
		m, err := load.MiscWithContext(ctx)
		if err != nil {
			return err
		}
		_ = reg.Collect("system.processes", now, map[string]float64{"running": float64(m.ProcsRunning), "blocked": float64(m.ProcsBlocked), "total": float64(m.ProcsTotal)})
		_ = reg.Collect("system.ctxt", now, map[string]float64{"switches": float64(m.Ctxt)})
	}
	return nil
}

// ---- memory ----

type memCollector struct{ hasSwap bool }

func (c *memCollector) Name() string { return "mem" }

func (c *memCollector) Init(reg *registry.Registry) error {
	if _, err := mem.VirtualMemory(); err != nil {
		return err
	}
	reg.AddChart(&registry.Chart{ID: "system.ram", Family: "ram", Title: "System RAM", Units: "MiB", Type: registry.Stacked,
		Priority: 300, Plugin: "system", Module: "mem",
		Dimensions: []*registry.Dimension{
			{ID: "free", Divisor: 1024 * 1024}, {ID: "used", Divisor: 1024 * 1024},
			{ID: "cached", Divisor: 1024 * 1024}, {ID: "buffers", Divisor: 1024 * 1024}}})
	reg.AddChart(&registry.Chart{ID: "mem.available", Family: "ram", Title: "Available RAM for applications", Units: "MiB", Type: registry.Area,
		Priority: 301, Plugin: "system", Module: "mem",
		Dimensions: []*registry.Dimension{{ID: "avail", Divisor: 1024 * 1024}}})
	reg.AddChart(&registry.Chart{ID: "mem.kernel", Family: "ram", Title: "Memory used by the kernel", Units: "MiB", Type: registry.Stacked,
		Priority: 302, Plugin: "system", Module: "mem",
		Dimensions: []*registry.Dimension{
			{ID: "slab", Divisor: 1024 * 1024}, {ID: "sunreclaim", Name: "unreclaimable", Divisor: 1024 * 1024},
			{ID: "page_tables", Divisor: 1024 * 1024}, {ID: "vmalloc_used", Divisor: 1024 * 1024}}})
	reg.AddChart(&registry.Chart{ID: "mem.writeback", Family: "ram", Title: "Writeback memory", Units: "MiB",
		Priority: 303, Plugin: "system", Module: "mem",
		Dimensions: []*registry.Dimension{{ID: "dirty", Divisor: 1024 * 1024}, {ID: "writeback", Divisor: 1024 * 1024}}})
	reg.AddChart(&registry.Chart{ID: "mem.committed", Family: "ram", Title: "Committed (overcommit) memory", Units: "MiB", Type: registry.Area,
		Priority: 304, Plugin: "system", Module: "mem",
		Dimensions: []*registry.Dimension{{ID: "committed", Divisor: 1024 * 1024}, {ID: "limit", Divisor: 1024 * 1024, Hidden: true}}})
	reg.AddChart(&registry.Chart{ID: "mem.pgfaults", Family: "ram", Title: "Memory page faults", Units: "faults/s",
		Priority: 305, Plugin: "system", Module: "mem",
		Dimensions: []*registry.Dimension{incDim("minor"), incDim("major")}})
	if sw, err := mem.SwapMemory(); err == nil && sw.Total > 0 {
		c.hasSwap = true
		reg.AddChart(&registry.Chart{ID: "mem.swap", Family: "swap", Title: "System swap", Units: "MiB", Type: registry.Stacked,
			Priority: 310, Plugin: "system", Module: "mem",
			Dimensions: []*registry.Dimension{{ID: "free", Divisor: 1024 * 1024}, {ID: "used", Divisor: 1024 * 1024}}})
		reg.AddChart(&registry.Chart{ID: "mem.swapio", Family: "swap", Title: "Swap I/O", Units: "KiB/s", Type: registry.Area,
			Priority: 311, Plugin: "system", Module: "mem",
			Dimensions: []*registry.Dimension{{ID: "in", Algorithm: registry.Incremental, Divisor: 1024}, {ID: "out", Algorithm: registry.Incremental, Multiplier: -1, Divisor: 1024}}})
	}
	return nil
}

func (c *memCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return err
	}
	used := vm.Total - vm.Free - vm.Cached - vm.Buffers
	if vm.Cached == 0 && vm.Buffers == 0 { // darwin/windows: no split available
		used = vm.Used
	}
	_ = reg.Collect("system.ram", now, map[string]float64{
		"free": float64(vm.Free), "used": float64(used), "cached": float64(vm.Cached), "buffers": float64(vm.Buffers)})
	_ = reg.Collect("mem.available", now, map[string]float64{"avail": float64(vm.Available)})
	_ = reg.Collect("mem.kernel", now, map[string]float64{
		"slab": float64(vm.Slab), "sunreclaim": float64(vm.Sunreclaim), "page_tables": float64(vm.PageTables), "vmalloc_used": float64(vm.VmallocUsed)})
	_ = reg.Collect("mem.writeback", now, map[string]float64{"dirty": float64(vm.Dirty), "writeback": float64(vm.WriteBack)})
	_ = reg.Collect("mem.committed", now, map[string]float64{"committed": float64(vm.CommittedAS), "limit": float64(vm.CommitLimit)})
	if sw, err := mem.SwapMemoryWithContext(ctx); err == nil {
		_ = reg.Collect("mem.pgfaults", now, map[string]float64{"minor": float64(sw.PgFault), "major": float64(sw.PgMajFault)})
	}
	if c.hasSwap {
		sw, err := mem.SwapMemoryWithContext(ctx)
		if err != nil {
			return err
		}
		_ = reg.Collect("mem.swap", now, map[string]float64{"free": float64(sw.Free), "used": float64(sw.Used)})
		_ = reg.Collect("mem.swapio", now, map[string]float64{"in": float64(sw.Sin), "out": float64(sw.Sout)})
	}
	return nil
}

// ---- disk io ----

type diskIOPrev struct {
	readBytes, writeBytes, readCount, writeCount, readTime, writeTime uint64
}

type diskCollector struct {
	known map[string]bool
	prev  map[string]diskIOPrev
}

func (c *diskCollector) Name() string { return "disk" }

func (c *diskCollector) Init(reg *registry.Registry) error {
	c.known = map[string]bool{}
	c.prev = map[string]diskIOPrev{}
	io, err := disk.IOCounters()
	if err != nil {
		return err
	}
	if len(io) == 0 {
		return fmt.Errorf("no block devices visible")
	}
	for name := range io {
		c.ensure(reg, name)
	}
	return nil
}

func isPartitionLike(name string) bool {
	// Linux: skip partitions (sda1, nvme0n1p1) and virtual devices (loop, ram, dm-)
	if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "zram") {
		return true
	}
	if strings.HasPrefix(name, "nvme") || strings.HasPrefix(name, "mmcblk") {
		return strings.Contains(name, "p")
	}
	if strings.HasPrefix(name, "sd") || strings.HasPrefix(name, "vd") || strings.HasPrefix(name, "xvd") || strings.HasPrefix(name, "hd") {
		return strings.ContainsAny(name, "0123456789")
	}
	return false
}

func (c *diskCollector) ensure(reg *registry.Registry, name string) {
	if c.known[name] || isPartitionLike(name) {
		return
	}
	c.known[name] = true
	lbl := map[string]string{"device": name}
	reg.AddChart(&registry.Chart{ID: "disk." + name, Context: "disk.io", Family: name, Title: "Disk I/O bandwidth", Units: "KiB/s", Type: registry.Area,
		Priority: 2000, Plugin: "system", Module: "disk", Labels: lbl,
		Dimensions: []*registry.Dimension{
			{ID: "reads", Algorithm: registry.Incremental, Divisor: 1024},
			{ID: "writes", Algorithm: registry.Incremental, Multiplier: -1, Divisor: 1024}}})
	reg.AddChart(&registry.Chart{ID: "disk_ops." + name, Context: "disk.ops", Family: name, Title: "Disk completed I/O operations", Units: "operations/s",
		Priority: 2001, Plugin: "system", Module: "disk", Labels: lbl,
		Dimensions: []*registry.Dimension{
			{ID: "reads", Algorithm: registry.Incremental},
			{ID: "writes", Algorithm: registry.Incremental, Multiplier: -1}}})
	reg.AddChart(&registry.Chart{ID: "disk_util." + name, Context: "disk.util", Family: name, Title: "Disk utilization time", Units: "% of time working", Type: registry.Area,
		Priority: 2002, Plugin: "system", Module: "disk", Labels: lbl,
		Dimensions: []*registry.Dimension{{ID: "utilization", Algorithm: registry.Incremental, Divisor: 10}}})
	reg.AddChart(&registry.Chart{ID: "disk_await." + name, Context: "disk.await", Family: name, Title: "Disk average completed I/O latency", Units: "milliseconds/operation",
		Priority: 2003, Plugin: "system", Module: "disk", Labels: lbl,
		Dimensions: []*registry.Dimension{{ID: "reads"}, {ID: "writes", Multiplier: -1}}})
	reg.AddChart(&registry.Chart{ID: "disk_avgsz." + name, Context: "disk.avgsz", Family: name, Title: "Disk average completed I/O amount", Units: "KiB/operation",
		Priority: 2004, Plugin: "system", Module: "disk", Labels: lbl,
		Dimensions: []*registry.Dimension{{ID: "reads", Divisor: 1024}, {ID: "writes", Multiplier: -1, Divisor: 1024}}})
}

func (c *diskCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	io, err := disk.IOCountersWithContext(ctx)
	if err != nil {
		return err
	}
	for name, st := range io {
		c.ensure(reg, name)
		if !c.known[name] {
			continue
		}
		_ = reg.Collect("disk."+name, now, map[string]float64{"reads": float64(st.ReadBytes), "writes": float64(st.WriteBytes)})
		_ = reg.Collect("disk_ops."+name, now, map[string]float64{"reads": float64(st.ReadCount), "writes": float64(st.WriteCount)})
		_ = reg.Collect("disk_util."+name, now, map[string]float64{"utilization": float64(st.IoTime)})
		cur := diskIOPrev{st.ReadBytes, st.WriteBytes, st.ReadCount, st.WriteCount, st.ReadTime, st.WriteTime}
		if prev, ok := c.prev[name]; ok {
			awaitR, awaitW := ratePerOp(st.ReadTime, prev.readTime, st.ReadCount, prev.readCount), ratePerOp(st.WriteTime, prev.writeTime, st.WriteCount, prev.writeCount)
			avgR, avgW := ratePerOp(st.ReadBytes, prev.readBytes, st.ReadCount, prev.readCount), ratePerOp(st.WriteBytes, prev.writeBytes, st.WriteCount, prev.writeCount)
			_ = reg.Collect("disk_await."+name, now, map[string]float64{"reads": awaitR, "writes": awaitW})
			_ = reg.Collect("disk_avgsz."+name, now, map[string]float64{"reads": avgR, "writes": avgW})
		}
		c.prev[name] = cur
	}
	return nil
}

type DiskRow struct {
	Device string `json:"device"`
	Reads  uint64 `json:"reads"`
	Writes uint64 `json:"writes"`
	ReadB  uint64 `json:"read_bytes"`
	WriteB uint64 `json:"write_bytes"`
	UtilMs uint64 `json:"util_ms"`
}

func (c *diskCollector) Functions() []Function {
	return []Function{{
		Name: "disks", Help: "Block devices (bytes, operations, busy time)", Timeout: 5,
		Run: func(ctx context.Context, _ map[string]string) (any, error) {
			io, err := disk.IOCountersWithContext(ctx)
			if err != nil {
				return Table{}, err
			}
			rows := make([]DiskRow, 0, len(io))
			for name, st := range io {
				if isPartitionLike(name) {
					continue
				}
				rows = append(rows, DiskRow{Device: name, Reads: st.ReadCount, Writes: st.WriteCount,
					ReadB: st.ReadBytes, WriteB: st.WriteBytes, UtilMs: st.IoTime})
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].Device < rows[j].Device })
			out := Table{Columns: []string{"device", "reads", "writes", "read_bytes", "write_bytes", "util_ms"}, Total: len(rows)}
			out.Rows = make([]any, len(rows))
			for i, r := range rows {
				out.Rows[i] = r
			}
			return out, nil
		},
	}}
}

// ratePerOp is (delta amount) / (delta operations); 0 when no ops completed.
func ratePerOp(curAmt, prevAmt, curOps, prevOps uint64) float64 {
	dOps := float64(curOps) - float64(prevOps)
	if dOps <= 0 {
		return 0
	}
	dAmt := float64(curAmt) - float64(prevAmt)
	if dAmt < 0 {
		return 0
	}
	return dAmt / dOps
}

// ---- disk space ----

type diskSpaceCollector struct{ mounts map[string]string }

func (c *diskSpaceCollector) Name() string { return "diskspace" }

var skipFS = map[string]bool{"tmpfs": true, "devtmpfs": true, "squashfs": true, "overlay": true, "proc": true, "sysfs": true,
	"cgroup": true, "cgroup2": true, "devpts": true, "autofs": true, "efivarfs": true, "fusectl": true, "debugfs": true,
	"tracefs": true, "securityfs": true, "pstore": true, "bpf": true, "configfs": true, "mqueue": true, "hugetlbfs": true,
	"binfmt_misc": true, "rpc_pipefs": true, "nsfs": true, "ramfs": true}

func mountID(mp string) string {
	if mp == "/" {
		return "_"
	}
	// Escape literal underscores first so "/srv/a_b" and "/srv/a/b" stay distinct.
	r := strings.NewReplacer("_", "__", "/", "_", "\\", "_", ":", "", " ", "_")
	return strings.Trim(r.Replace(mp), "_")
}

func (c *diskSpaceCollector) Init(reg *registry.Registry) error {
	c.mounts = map[string]string{}
	parts, err := disk.Partitions(false)
	if err != nil {
		return err
	}
	for _, p := range parts {
		if skipFS[p.Fstype] || strings.HasPrefix(p.Mountpoint, "/snap/") || strings.HasPrefix(p.Mountpoint, "/System/Volumes/") {
			continue
		}
		u, err := disk.Usage(p.Mountpoint)
		if err != nil || u.Total == 0 {
			continue
		}
		c.ensure(reg, p.Mountpoint, u.InodesTotal > 0)
	}
	if len(c.mounts) == 0 {
		return fmt.Errorf("no mount points")
	}
	return nil
}

func (c *diskSpaceCollector) ensure(reg *registry.Registry, mp string, inodes bool) {
	if _, ok := c.mounts[mp]; ok {
		return
	}
	id := mountID(mp)
	c.mounts[mp] = id
	lbl := map[string]string{"mount_point": mp}
	reg.AddChart(&registry.Chart{ID: "disk_space." + id, Context: "disk.space", Family: mp, Title: "Disk space usage", Units: "GiB", Type: registry.Stacked,
		Priority: 2100, Plugin: "system", Module: "diskspace", Labels: lbl,
		Dimensions: []*registry.Dimension{{ID: "avail", Divisor: 1024 * 1024 * 1024}, {ID: "used", Divisor: 1024 * 1024 * 1024}}})
	if inodes {
		reg.AddChart(&registry.Chart{ID: "disk_inodes." + id, Context: "disk.inodes", Family: mp, Title: "Disk files (inodes) usage", Units: "inodes", Type: registry.Stacked,
			Priority: 2101, Plugin: "system", Module: "diskspace", Labels: lbl,
			Dimensions: []*registry.Dimension{{ID: "avail"}, {ID: "used"}}})
	}
}

func (c *diskSpaceCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	for mp, id := range c.mounts {
		u, err := disk.UsageWithContext(ctx, mp)
		if err != nil {
			continue
		}
		_ = reg.Collect("disk_space."+id, now, map[string]float64{"avail": float64(u.Free), "used": float64(u.Used)})
		if _, ok := reg.Chart("disk_inodes." + id); ok {
			_ = reg.Collect("disk_inodes."+id, now, map[string]float64{"avail": float64(u.InodesFree), "used": float64(u.InodesUsed)})
		}
	}
	return nil
}

type MountRow struct {
	Mount  string  `json:"mount"`
	Device string  `json:"device"`
	FS     string  `json:"fs"`
	Total  uint64  `json:"total"`
	Used   uint64  `json:"used"`
	Avail  uint64  `json:"avail"`
	Pct    float64 `json:"used_percent"`
}

func (c *diskSpaceCollector) Functions() []Function {
	return []Function{{
		Name: "mounts", Help: "Mounted filesystems (capacity and usage)", Timeout: 5,
		Run: func(ctx context.Context, _ map[string]string) (any, error) {
			parts, err := disk.PartitionsWithContext(ctx, false)
			if err != nil {
				return Table{}, err
			}
			rows := make([]MountRow, 0, len(parts))
			for _, p := range parts {
				if skipFS[p.Fstype] {
					continue
				}
				u, err := disk.UsageWithContext(ctx, p.Mountpoint)
				if err != nil || u.Total == 0 {
					continue
				}
				rows = append(rows, MountRow{Mount: p.Mountpoint, Device: p.Device, FS: p.Fstype,
					Total: u.Total, Used: u.Used, Avail: u.Free, Pct: u.UsedPercent})
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].Mount < rows[j].Mount })
			out := Table{Columns: []string{"mount", "device", "fs", "total", "used", "avail", "used_percent"}, Total: len(rows)}
			out.Rows = make([]any, len(rows))
			for i, r := range rows {
				out.Rows[i] = r
			}
			return out, nil
		},
	}}
}

// ---- network ----

type netCollector struct{ known map[string]bool }

func (c *netCollector) Name() string { return "net" }

func (c *netCollector) Init(reg *registry.Registry) error {
	c.known = map[string]bool{}
	ifs, err := net.IOCounters(true)
	if err != nil {
		return err
	}
	for _, i := range ifs {
		c.ensure(reg, i.Name)
	}
	if len(c.known) == 0 {
		return fmt.Errorf("no network interfaces")
	}
	return nil
}

func (c *netCollector) ensure(reg *registry.Registry, name string) {
	if c.known[name] || name == "lo" || name == "lo0" || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "docker") ||
		strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "utun") || strings.HasPrefix(name, "awdl") || strings.HasPrefix(name, "llw") {
		return
	}
	c.known[name] = true
	lbl := map[string]string{"interface": name}
	reg.AddChart(&registry.Chart{ID: "net." + name, Context: "net.net", Family: name, Title: "Bandwidth", Units: "kilobits/s", Type: registry.Area,
		Priority: 3000, Plugin: "system", Module: "net", Labels: lbl,
		Dimensions: []*registry.Dimension{
			{ID: "received", Algorithm: registry.Incremental, Multiplier: 8, Divisor: 1000},
			{ID: "sent", Algorithm: registry.Incremental, Multiplier: -8, Divisor: 1000}}})
	reg.AddChart(&registry.Chart{ID: "net_packets." + name, Context: "net.packets", Family: name, Title: "Packets", Units: "packets/s",
		Priority: 3001, Plugin: "system", Module: "net", Labels: lbl,
		Dimensions: []*registry.Dimension{
			{ID: "received", Algorithm: registry.Incremental},
			{ID: "sent", Algorithm: registry.Incremental, Multiplier: -1}}})
	reg.AddChart(&registry.Chart{ID: "net_errors." + name, Context: "net.errors", Family: name, Title: "Interface errors", Units: "errors/s",
		Priority: 3002, Plugin: "system", Module: "net", Labels: lbl,
		Dimensions: []*registry.Dimension{
			{ID: "inbound", Algorithm: registry.Incremental},
			{ID: "outbound", Algorithm: registry.Incremental, Multiplier: -1}}})
	reg.AddChart(&registry.Chart{ID: "net_drops." + name, Context: "net.drops", Family: name, Title: "Interface drops", Units: "drops/s",
		Priority: 3003, Plugin: "system", Module: "net", Labels: lbl,
		Dimensions: []*registry.Dimension{
			{ID: "inbound", Algorithm: registry.Incremental},
			{ID: "outbound", Algorithm: registry.Incremental, Multiplier: -1}}})
}

func (c *netCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	ifs, err := net.IOCountersWithContext(ctx, true)
	if err != nil {
		return err
	}
	for _, i := range ifs {
		c.ensure(reg, i.Name)
		if !c.known[i.Name] {
			continue
		}
		_ = reg.Collect("net."+i.Name, now, map[string]float64{"received": float64(i.BytesRecv), "sent": float64(i.BytesSent)})
		_ = reg.Collect("net_packets."+i.Name, now, map[string]float64{"received": float64(i.PacketsRecv), "sent": float64(i.PacketsSent)})
		_ = reg.Collect("net_errors."+i.Name, now, map[string]float64{"inbound": float64(i.Errin), "outbound": float64(i.Errout)})
		_ = reg.Collect("net_drops."+i.Name, now, map[string]float64{"inbound": float64(i.Dropin), "outbound": float64(i.Dropout)})
	}
	return nil
}

type IfaceRow struct {
	Name   string `json:"name"`
	Rx     uint64 `json:"rx_bytes"`
	Tx     uint64 `json:"tx_bytes"`
	RxPkt  uint64 `json:"rx_packets"`
	TxPkt  uint64 `json:"tx_packets"`
	RxErr  uint64 `json:"rx_errors"`
	TxErr  uint64 `json:"tx_errors"`
	RxDrop uint64 `json:"rx_drops"`
	TxDrop uint64 `json:"tx_drops"`
}

func (c *netCollector) Functions() []Function {
	return []Function{{
		Name: "network-interfaces", Help: "Network interface counters (bytes, packets, errors, drops)", Timeout: 5,
		Run: func(ctx context.Context, _ map[string]string) (any, error) {
			ifs, err := net.IOCountersWithContext(ctx, true)
			if err != nil {
				return Table{}, err
			}
			rows := make([]IfaceRow, 0, len(ifs))
			for _, i := range ifs {
				rows = append(rows, IfaceRow{Name: i.Name, Rx: i.BytesRecv, Tx: i.BytesSent,
					RxPkt: i.PacketsRecv, TxPkt: i.PacketsSent, RxErr: i.Errin, TxErr: i.Errout,
					RxDrop: i.Dropin, TxDrop: i.Dropout})
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
			out := Table{Columns: []string{"name", "rx_bytes", "tx_bytes", "rx_packets", "tx_packets", "rx_errors", "tx_errors", "rx_drops", "tx_drops"}, Total: len(rows)}
			out.Rows = make([]any, len(rows))
			for i, r := range rows {
				out.Rows[i] = r
			}
			return out, nil
		},
	}}
}

// ---- uptime ----

type uptimeCollector struct{}

func (c *uptimeCollector) Name() string { return "uptime" }

func (c *uptimeCollector) Init(reg *registry.Registry) error {
	if _, err := host.Uptime(); err != nil {
		return err
	}
	reg.AddChart(&registry.Chart{ID: "system.uptime", Family: "uptime", Title: "System uptime", Units: "seconds",
		Priority: 900, Plugin: "system", Module: "uptime", Dimensions: []*registry.Dimension{{ID: "uptime"}}})
	return nil
}

func (c *uptimeCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	u, err := host.UptimeWithContext(ctx)
	if err != nil {
		return err
	}
	return reg.Collect("system.uptime", now, map[string]float64{"uptime": float64(u)})
}
