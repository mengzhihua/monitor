package collect

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// freebsdConfig is collectors.modules.freebsd (sysctl, Netdata freebsd.plugin).
type freebsdConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
	IPFW    string        `yaml:"ipfw"`
	Gstat   string        `yaml:"gstat"`
	DF      string        `yaml:"df"`
	Netstat string        `yaml:"netstat"`
}

type freebsdCollector struct {
	cfg  freebsdConfig
	read func(ctx context.Context) (map[string]string, error)
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	cpus []string

	// Optional CLI dumps (tests inject; live collection shells out).
	ipfwList, gstatOut, dfOut, netstatOut []byte
	ipfwSeen, diskSeen, ifSeen, mntSeen   map[string]bool
	irqSeen                               map[string]bool
}

func init() {
	Register("freebsd", func() Collector { return &freebsdCollector{} })
}

func (f *freebsdCollector) Name() string { return "freebsd" }

func (f *freebsdCollector) Configure(decode func(v any) error) error {
	if err := decode(&f.cfg); err != nil {
		return err
	}
	if f.cfg.Command == "" {
		f.cfg.Command = "sysctl"
	}
	if f.cfg.Timeout <= 0 {
		f.cfg.Timeout = 3 * time.Second
	}
	if f.cfg.IPFW == "" {
		f.cfg.IPFW = "ipfw"
	}
	if f.cfg.Gstat == "" {
		f.cfg.Gstat = "gstat"
	}
	if f.cfg.DF == "" {
		f.cfg.DF = "df"
	}
	if f.cfg.Netstat == "" {
		f.cfg.Netstat = "netstat"
	}
	return nil
}

func (f *freebsdCollector) Init(reg *registry.Registry) error {
	if f.cfg.Command == "" {
		if err := f.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if f.read == nil && runtime.GOOS != "freebsd" {
		return errors.New("freebsd collector is freebsd-only")
	}
	m, err := f.sysctls(context.Background())
	if err != nil {
		return err
	}
	if !freebsdHasMetrics(m) {
		return fmt.Errorf("freebsd: no sysctl metrics")
	}
	f.addCharts(reg, m)
	f.addChartsM22(reg, m)
	return nil
}

func (f *freebsdCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := f.sysctls(ctx)
	if err != nil {
		return err
	}
	page := sysctlUint(m, "hw.pagesize", "vm.stats.vm.v_page_size")
	if page == 0 {
		page = 4096
	}
	if _, ok := reg.Chart("system.ctxt"); ok {
		_ = reg.Collect("system.ctxt", now, map[string]float64{"switches": sysctlUint(m, "vm.stats.sys.v_swtch")})
	}
	if _, ok := reg.Chart("system.intr"); ok {
		_ = reg.Collect("system.intr", now, map[string]float64{"interrupts": sysctlUint(m, "vm.stats.sys.v_intr")})
	}
	if _, ok := reg.Chart("system.softirq"); ok {
		_ = reg.Collect("system.softirq", now, map[string]float64{"softirq": sysctlUint(m, "vm.stats.sys.v_soft")})
	}
	if _, ok := reg.Chart("system.forks"); ok {
		_ = reg.Collect("system.forks", now, map[string]float64{"started": sysctlUint(m, "vm.stats.vm.v_forks")})
	}
	if _, ok := reg.Chart("mem.wired"); ok {
		_ = reg.Collect("mem.wired", now, map[string]float64{"wired": sysctlUint(m, "vm.stats.vm.v_wire_count") * page})
	}
	if _, ok := reg.Chart("mem.laundry"); ok {
		_ = reg.Collect("mem.laundry", now, map[string]float64{"laundry": sysctlUint(m, "vm.stats.vm.v_laundry_count") * page})
	}
	if _, ok := reg.Chart("system.ipc_semaphores"); ok {
		_ = reg.Collect("system.ipc_semaphores", now, map[string]float64{
			"semaphores": sysctlUint(m, "kern.ipc.semusz"),
			"max":        sysctlUint(m, "kern.ipc.semmni"),
		})
	}
	if _, ok := reg.Chart("system.ipc_semaphore_values"); ok {
		_ = reg.Collect("system.ipc_semaphore_values", now, map[string]float64{
			"semaphores": sysctlUint(m, "kern.ipc.semaem"),
			"max":        sysctlUint(m, "kern.ipc.semmns"),
		})
	}
	if _, ok := reg.Chart("system.ipc_shared_mem_segs"); ok {
		_ = reg.Collect("system.ipc_shared_mem_segs", now, map[string]float64{
			"segments": sysctlUint(m, "kern.ipc.shm_nused"),
			"max":      sysctlUint(m, "kern.ipc.shmmni"),
		})
	}
	if _, ok := reg.Chart("system.ipc_shared_mem_size"); ok {
		_ = reg.Collect("system.ipc_shared_mem_size", now, map[string]float64{"allocated": sysctlUint(m, "kern.ipc.shmmax")})
	}
	if _, ok := reg.Chart("system.ipc_msq_queues"); ok {
		_ = reg.Collect("system.ipc_msq_queues", now, map[string]float64{
			"queues": sysctlUint(m, "kern.ipc.msgmni"),
		})
	}
	if _, ok := reg.Chart("system.ipc_msq_messages"); ok {
		_ = reg.Collect("system.ipc_msq_messages", now, map[string]float64{"messages": sysctlUint(m, "kern.ipc.msgtql")})
	}
	if _, ok := reg.Chart("freebsd.cpu.temperature"); ok {
		vals := map[string]float64{}
		var hottest float64
		for _, id := range f.cpus {
			v := firstFloat(m["dev.cpu."+strings.TrimPrefix(id, "cpu")+".temperature"])
			vals[id] = v
			if v > hottest {
				hottest = v
			}
		}
		vals["hottest"] = hottest
		_ = reg.Collect("freebsd.cpu.temperature", now, vals)
	}
	f.collectM22(ctx, reg, now, m)
	return nil
}

func (f *freebsdCollector) addCharts(reg *registry.Registry, m map[string]string) {
	plugin := "freebsd"
	add := func(ch *registry.Chart) {
		if _, ok := reg.Chart(ch.ID); ok {
			return
		}
		if ch.Plugin == "" {
			ch.Plugin = plugin
		}
		if ch.Module == "" {
			ch.Module = "sysctl"
		}
		reg.AddChart(ch)
	}
	if sysctlHas(m, "vm.stats.sys.v_swtch") {
		add(&registry.Chart{ID: "system.ctxt", Family: "processes", Title: "CPU context switches", Units: "switches/s",
			Priority: 211, Dimensions: []*registry.Dimension{incDim("switches")}})
	}
	if sysctlHas(m, "vm.stats.sys.v_intr") {
		add(&registry.Chart{ID: "system.intr", Family: "processes", Title: "CPU interrupts", Units: "interrupts/s",
			Priority: 212, Dimensions: []*registry.Dimension{incDim("interrupts")}})
	}
	if sysctlHas(m, "vm.stats.sys.v_soft") {
		add(&registry.Chart{ID: "system.softirq", Family: "processes", Title: "CPU software interrupts", Units: "interrupts/s",
			Priority: 214, Dimensions: []*registry.Dimension{incDim("softirq")}})
	}
	if sysctlHas(m, "vm.stats.vm.v_forks") {
		add(&registry.Chart{ID: "system.forks", Family: "processes", Title: "Started processes", Units: "processes/s",
			Priority: 213, Dimensions: []*registry.Dimension{incDim("started")}})
	}
	if sysctlHas(m, "vm.stats.vm.v_wire_count") {
		add(&registry.Chart{ID: "mem.wired", Family: "ram", Title: "Wired memory", Units: "MiB", Type: registry.Area,
			Priority: 306, Dimensions: []*registry.Dimension{{ID: "wired", Divisor: 1024 * 1024}}})
	}
	if sysctlHas(m, "vm.stats.vm.v_laundry_count") {
		add(&registry.Chart{ID: "mem.laundry", Family: "ram", Title: "Laundry memory", Units: "MiB", Type: registry.Area,
			Priority: 307, Dimensions: []*registry.Dimension{{ID: "laundry", Divisor: 1024 * 1024}}})
	}
	if sysctlHas(m, "kern.ipc.semusz", "kern.ipc.semmni") {
		add(&registry.Chart{ID: "system.ipc_semaphores", Family: "ipc", Title: "IPC semaphores", Units: "semaphores",
			Priority: 5000, Dimensions: []*registry.Dimension{{ID: "semaphores"}, {ID: "max", Hidden: true}}})
	}
	if sysctlHas(m, "kern.ipc.semaem", "kern.ipc.semmns") {
		add(&registry.Chart{ID: "system.ipc_semaphore_values", Family: "ipc", Title: "IPC semaphore values", Units: "semaphores",
			Priority: 5001, Dimensions: []*registry.Dimension{{ID: "semaphores"}, {ID: "max", Hidden: true}}})
	}
	if sysctlHas(m, "kern.ipc.shm_nused", "kern.ipc.shmmni") {
		add(&registry.Chart{ID: "system.ipc_shared_mem_segs", Family: "ipc", Title: "IPC shared memory segments", Units: "segments",
			Priority: 5002, Dimensions: []*registry.Dimension{{ID: "segments"}, {ID: "max", Hidden: true}}})
	}
	if sysctlHas(m, "kern.ipc.shmmax") {
		add(&registry.Chart{ID: "system.ipc_shared_mem_size", Family: "ipc", Title: "IPC shared memory size", Units: "bytes",
			Priority: 5003, Dimensions: []*registry.Dimension{{ID: "allocated"}}})
	}
	if sysctlHas(m, "kern.ipc.msgmni") {
		add(&registry.Chart{ID: "system.ipc_msq_queues", Family: "ipc", Title: "IPC message queues", Units: "queues",
			Priority: 5004, Dimensions: []*registry.Dimension{{ID: "queues"}}})
	}
	if sysctlHas(m, "kern.ipc.msgtql") {
		add(&registry.Chart{ID: "system.ipc_msq_messages", Family: "ipc", Title: "IPC message queue messages", Units: "messages",
			Priority: 5005, Dimensions: []*registry.Dimension{{ID: "messages"}}})
	}
	f.cpus = freebsdCPUTemps(m)
	if len(f.cpus) > 0 {
		dims := make([]*registry.Dimension, 0, len(f.cpus)+1)
		for _, id := range f.cpus {
			dims = append(dims, &registry.Dimension{ID: id})
		}
		dims = append(dims, &registry.Dimension{ID: "hottest"})
		add(&registry.Chart{ID: "freebsd.cpu.temperature", Context: "freebsd.cpu.temperature", Family: "cpu",
			Title: "CPU temperature", Units: "Celsius", Priority: 5100, Dimensions: dims})
	}
}

func (f *freebsdCollector) sysctls(ctx context.Context) (map[string]string, error) {
	if f.read != nil {
		return f.read(ctx)
	}
	run := execRun(f.cfg.Timeout)
	out, err := run(ctx, f.cfg.Command, append([]string{"-e"}, freebsdSysctlKeys...)...)
	if err != nil {
		out, err = run(ctx, f.cfg.Command, "-a")
		if err != nil {
			return nil, fmt.Errorf("sysctl: %w", err)
		}
	}
	m := parseSysctl(out)
	if temps, err := run(ctx, f.cfg.Command, "-e", "dev.cpu.0.temperature", "dev.cpu.1.temperature",
		"dev.cpu.2.temperature", "dev.cpu.3.temperature", "dev.cpu.4.temperature", "dev.cpu.5.temperature",
		"dev.cpu.6.temperature", "dev.cpu.7.temperature"); err == nil {
		for k, v := range parseSysctl(temps) {
			m[k] = v
		}
	}
	for _, p := range freebsdSysctlPrefixes {
		f.mergeSysctl(ctx, m, "-e", p)
	}
	if f.read == nil {
		f.mergeBinaryTCP(ctx, m)
	}
	return m, nil
}

func (f *freebsdCollector) mergeBinaryTCP(ctx context.Context, m map[string]string) {
	if sysctlHasPrefix(m, "net.inet.tcp.stats.") {
		return
	}
	run := f.run
	if run == nil {
		run = execRun(f.cfg.Timeout)
	}
	out, err := run(ctx, f.cfg.Command, "-b", "net.inet.tcp.stats")
	if err != nil || len(out) < 8 {
		return
	}
	for k, v := range decodeTCPStat(out) {
		m[k] = v
	}
}

var freebsdSysctlKeys = []string{
	"hw.pagesize", "vm.stats.vm.v_page_size",
	"vm.stats.sys.v_swtch", "vm.stats.sys.v_intr", "vm.stats.sys.v_soft",
	"vm.stats.sys.v_syscall", "vm.stats.sys.v_syscalls",
	"vm.stats.vm.v_forks", "vm.stats.vm.v_wire_count", "vm.stats.vm.v_laundry_count",
	"vm.stats.vm.v_free_count", "vm.stats.vm.v_active_count", "vm.stats.vm.v_inactive_count",
	"vm.stats.vm.v_cache_count", "vm.stats.vm.v_vm_faults", "vm.stats.vm.v_io_faults",
	"vm.stats.vm.v_cow_faults", "vm.stats.vm.v_cow_optim", "vm.stats.vm.v_intrans",
	"vm.stats.vm.v_swappgsin", "vm.stats.vm.v_swappgsout",
	"kern.ipc.semmni", "kern.ipc.semmns", "kern.ipc.semusz", "kern.ipc.semaem",
	"kern.ipc.shmmni", "kern.ipc.shm_nused", "kern.ipc.shmmax",
	"kern.ipc.msgmni", "kern.ipc.msgtql",
	"kstat.zfs.misc.arcstats.size", "dev.cpu.0.freq",
	"vfs.bufspace", "hw.intrcnt", "hw.intrnames",
	"net.isr.dispatched", "net.isr.hybrid_dispatched", "net.isr.qdrops", "net.isr.queued",
	"net.inet.tcp.states", "net.inet.ip.fw.dyn_count", "net.inet.ip.fw.enable",
}

func parseSysctl(b []byte) map[string]string {
	m := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sep := "="
		i := strings.IndexByte(line, '=')
		if i < 0 {
			i = strings.IndexByte(line, ':')
			sep = ":"
		}
		if i <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:i])
		v := strings.TrimSpace(strings.TrimPrefix(line[i:], sep))
		m[k] = v
	}
	return m
}

func sysctlUint(m map[string]string, keys ...string) float64 {
	for _, k := range keys {
		if s, ok := m[k]; ok && s != "" {
			return firstFloat(s)
		}
	}
	return 0
}

func sysctlHas(m map[string]string, keys ...string) bool {
	for _, k := range keys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

func freebsdHasMetrics(m map[string]string) bool {
	for _, k := range freebsdSysctlKeys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	if len(freebsdCPUTemps(m)) > 0 {
		return true
	}
	for _, p := range freebsdSysctlPrefixes {
		if sysctlHas(m, p) || sysctlHasPrefix(m, p+".") {
			return true
		}
	}
	return false
}

func freebsdCPUTemps(m map[string]string) []string {
	var ids []string
	for k := range m {
		rest, ok := strings.CutPrefix(k, "dev.cpu.")
		if !ok || !strings.HasSuffix(rest, ".temperature") {
			continue
		}
		n := strings.TrimSuffix(rest, ".temperature")
		if _, err := strconv.Atoi(n); err != nil {
			continue
		}
		ids = append(ids, "cpu"+n)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, _ := strconv.Atoi(strings.TrimPrefix(ids[i], "cpu"))
		b, _ := strconv.Atoi(strings.TrimPrefix(ids[j], "cpu"))
		return a < b
	})
	return ids
}
