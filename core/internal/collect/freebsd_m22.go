package collect

import (
	"bufio"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// freebsdSysctlPrefixes are extra sysctl subtrees fetched after the M16 key list
// (so a successful short -e does not skip ZFS / net.inet / hw.intrcnt).
var freebsdSysctlPrefixes = []string{
	"kstat.zfs.misc.arcstats",
	"kstat.zfs.misc.zio_trim",
	"vm.stats.vm",
	"vm.stats.sys",
	"vm.loadavg",
	"vm.swap_total",
	"kern.cp_time",
	"dev.cpu.0.freq",
	"hw.intrcnt",
	"hw.intrnames",
	"net.isr.numthreads",
	"net.isr.dispatched",
	"net.isr.hybrid_dispatched",
	"net.isr.qdrops",
	"net.isr.queued",
	"net.isr.dispatch",
	"net.inet.tcp.states",
	"net.inet.tcp.stats",
	"net.inet.udp.stats",
	"net.inet.icmp.stats",
	"net.inet.ip.stats",
	"net.inet6.ip6.stats",
	"net.inet6.icmp6.stats",
	"net.inet.ip.fw.dyn_count",
	"net.inet.ip.fw.enable",
}

func (f *freebsdCollector) runner() func(ctx context.Context, name string, args ...string) ([]byte, error) {
	if f.run != nil {
		return f.run
	}
	return execRun(f.cfg.Timeout)
}

func (f *freebsdCollector) mergeSysctl(ctx context.Context, m map[string]string, args ...string) {
	run := f.runner()
	out, err := run(ctx, f.cfg.Command, args...)
	if err != nil {
		return
	}
	for k, v := range parseSysctl(out) {
		m[k] = v
	}
}

func (f *freebsdCollector) addChartsM22(reg *registry.Registry, m map[string]string) {
	if f.ipfwSeen == nil {
		f.ipfwSeen, f.diskSeen, f.ifSeen, f.mntSeen, f.irqSeen =
			map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	}

	if sysctlHas(m, "vm.stats.sys.v_syscall", "vm.stats.sys.v_syscalls") {
		f.ensureChart(reg, &registry.Chart{ID: "system.syscalls", Family: "processes", Title: "System calls",
			Units: "calls/s", Priority: 215, Module: "sysctl", Dimensions: []*registry.Dimension{incDim("calls")}})
	}
	if sysctlHas(m, "vm.stats.vm.v_vm_faults", "vm.stats.vm.v_io_faults") {
		ch := f.ensureChart(reg, &registry.Chart{ID: "mem.pgfaults", Family: "ram", Title: "Memory page faults",
			Units: "page faults/s", Priority: 305, Module: "sysctl", Dimensions: []*registry.Dimension{
				incDim("memory"), incDim("io_requiring"), incDim("cow"), incDim("cow_optimized"), incDim("in_transit"),
				incDim("minor"), incDim("major"),
			}})
		for _, d := range []*registry.Dimension{incDim("memory"), incDim("io_requiring"), incDim("cow"), incDim("cow_optimized"), incDim("in_transit")} {
			ch.AddDimension(d)
		}
	}
	if sysctlHas(m, "vm.stats.vm.v_swappgsin", "vm.stats.vm.v_swappgsout") {
		f.ensureChart(reg, &registry.Chart{ID: "mem.swapio", Family: "swap", Title: "Swap I/O", Units: "KiB/s",
			Type: registry.Area, Priority: 311, Module: "sysctl", Dimensions: []*registry.Dimension{
				{ID: "in", Algorithm: registry.Incremental, Divisor: 1024},
				{ID: "out", Algorithm: registry.Incremental, Multiplier: -1, Divisor: 1024},
			}})
	}
	if sysctlHas(m, "vm.stats.vm.v_free_count", "vm.stats.vm.v_active_count") {
		ch := f.ensureChart(reg, &registry.Chart{ID: "system.ram", Family: "ram", Title: "System RAM", Units: "MiB",
			Type: registry.Stacked, Priority: 300, Module: "sysctl", Dimensions: []*registry.Dimension{
				{ID: "free", Divisor: 1024 * 1024}, {ID: "active", Divisor: 1024 * 1024},
				{ID: "inactive", Divisor: 1024 * 1024}, {ID: "wired", Divisor: 1024 * 1024},
				{ID: "cache", Divisor: 1024 * 1024}, {ID: "laundry", Divisor: 1024 * 1024},
				{ID: "buffers", Divisor: 1024 * 1024}, {ID: "used", Divisor: 1024 * 1024},
				{ID: "cached", Divisor: 1024 * 1024},
			}})
		for _, id := range []string{"free", "active", "inactive", "wired", "cache", "laundry", "buffers"} {
			ch.AddDimension(&registry.Dimension{ID: id, Divisor: 1024 * 1024})
		}
	}
	if sysctlHas(m, "vm.stats.vm.v_free_count") {
		f.ensureChart(reg, &registry.Chart{ID: "mem.available", Family: "ram", Title: "Available RAM for applications",
			Units: "MiB", Type: registry.Area, Priority: 301, Module: "sysctl",
			Dimensions: []*registry.Dimension{{ID: "avail", Divisor: 1024 * 1024}}})
	}
	if sysctlHas(m, "vm.vmtotal.t_rq", "vm.stats.vm.v_active_count") {
		f.ensureChart(reg, &registry.Chart{ID: "system.active_processes", Family: "processes", Title: "System active processes",
			Units: "processes", Priority: 216, Module: "sysctl", Dimensions: []*registry.Dimension{{ID: "active"}}})
	}
	if sysctlHas(m, "dev.cpu.0.freq") {
		f.ensureChart(reg, &registry.Chart{ID: "cpu.scaling_cur_freq", Family: "cpufreq", Title: "Current CPU scaling frequency",
			Units: "MHz", Priority: 1010, Module: "sysctl", Dimensions: []*registry.Dimension{{ID: "frequency"}}})
	}
	if sysctlHas(m, "hw.intrcnt", "hw.intrnames") {
		f.ensureChart(reg, &registry.Chart{ID: "system.interrupts", Family: "interrupts", Title: "System interrupts",
			Units: "interrupts/s", Priority: 1000, Module: "hw.intrcnt", Dimensions: []*registry.Dimension{}})
	}
	if sysctlHas(m, "net.isr.numthreads", "net.isr.dispatched", "net.isr.qdrops") {
		f.ensureChart(reg, &registry.Chart{ID: "system.softnet_stat", Family: "softnet_stat", Title: "System softnet_stat",
			Units: "events/s", Priority: 955, Module: "net.isr", Dimensions: []*registry.Dimension{
				incDim("dispatched"), incDim("hybrid_dispatched"), incDim("qdrops"), incDim("queued"),
			}})
	}
	if sysctlHas(m, "net.inet.tcp.states") || sysctlHas(m, "net.inet.tcp.stats.tcps_rcvtotal", "tcps_rcvtotal") {
		f.ensureChart(reg, &registry.Chart{ID: "ipv4.tcpsock", Family: "tcp", Title: "IPv4 TCP connections",
			Units: "active connections", Priority: 2500, Module: "net.inet.tcp.states",
			Dimensions: []*registry.Dimension{{ID: "CurrEstab", Name: "connections"}}})
	}
	if sysctlHasPrefix(m, "net.inet.tcp.stats.") || sysctlHas(m, "tcps_rcvtotal") {
		f.addIPv4TCPCharts(reg)
	}
	if sysctlHasPrefix(m, "net.inet.udp.stats.") || sysctlHas(m, "udps_ipackets") {
		f.addIPv4UDPCharts(reg)
	}
	if sysctlHasPrefix(m, "net.inet.icmp.stats.") || sysctlHas(m, "icps_inhist") {
		f.addIPv4ICMPCharts(reg)
	}
	if sysctlHasPrefix(m, "net.inet.ip.stats.") || sysctlHas(m, "ips_total") {
		f.addIPv4IPCharts(reg)
	}
	if sysctlHasPrefix(m, "net.inet6.ip6.stats.") || sysctlHas(m, "ip6s_total") {
		f.addIPv6Charts(reg)
	}
	if sysctlHasPrefix(m, "kstat.zfs.misc.arcstats.") || sysctlHas(m, "kstat.zfs.misc.arcstats.size") {
		f.addZFSChartsFBSD(reg, m)
	}
}

func (f *freebsdCollector) addIPv4TCPCharts(reg *registry.Registry) {
	mod := "net.inet.tcp.stats"
	f.ensureChart(reg, &registry.Chart{ID: "ipv4.tcppackets", Family: "tcp", Title: "IPv4 TCP packets",
		Units: "packets/s", Priority: 2600, Module: mod, Dimensions: []*registry.Dimension{
			incDim("InSegs"), {ID: "OutSegs", Algorithm: registry.Incremental, Multiplier: -1},
		}})
	f.ensureChart(reg, &registry.Chart{ID: "ipv4.tcperrors", Family: "tcp", Title: "IPv4 TCP errors",
		Units: "packets/s", Priority: 2700, Module: mod, Dimensions: []*registry.Dimension{
			incDim("InErrs"), incDim("InCsumErrors"), {ID: "RetransSegs", Algorithm: registry.Incremental, Multiplier: -1},
		}})
	f.ensureChart(reg, &registry.Chart{ID: "ipv4.tcphandshake", Family: "tcp", Title: "IPv4 TCP handshake issues",
		Units: "events/s", Priority: 2800, Module: mod, Dimensions: []*registry.Dimension{
			incDim("EstabResets"), incDim("ActiveOpens"), incDim("PassiveOpens"), incDim("AttemptFails"),
		}})
}

func (f *freebsdCollector) addIPv4UDPCharts(reg *registry.Registry) {
	mod := "net.inet.udp.stats"
	f.ensureChart(reg, &registry.Chart{ID: "ipv4.udppackets", Family: "udp", Title: "IPv4 UDP packets",
		Units: "packets/s", Priority: 2601, Module: mod, Dimensions: []*registry.Dimension{
			incDim("InDatagrams"), {ID: "OutDatagrams", Algorithm: registry.Incremental, Multiplier: -1},
		}})
	f.ensureChart(reg, &registry.Chart{ID: "ipv4.udperrors", Family: "udp", Title: "IPv4 UDP errors",
		Units: "events/s", Priority: 2701, Module: mod, Dimensions: []*registry.Dimension{
			incDim("InErrors"), incDim("NoPorts"), incDim("RcvbufErrors"), incDim("SndbufErrors"), incDim("InCsumErrors"),
		}})
}

func (f *freebsdCollector) addIPv4ICMPCharts(reg *registry.Registry) {
	mod := "net.inet.icmp.stats"
	f.ensureChart(reg, &registry.Chart{ID: "ipv4.icmp", Family: "icmp", Title: "IPv4 ICMP packets",
		Units: "packets/s", Priority: 2602, Module: mod, Dimensions: []*registry.Dimension{
			incDim("InMsgs"), {ID: "OutMsgs", Algorithm: registry.Incremental, Multiplier: -1},
		}})
	f.ensureChart(reg, &registry.Chart{ID: "ipv4.icmp_errors", Family: "icmp", Title: "IPv4 ICMP errors",
		Units: "packets/s", Priority: 2702, Module: mod, Dimensions: []*registry.Dimension{incDim("InErrors"), incDim("OutErrors")}})
}

func (f *freebsdCollector) addIPv4IPCharts(reg *registry.Registry) {
	mod := "net.inet.ip.stats"
	f.ensureChart(reg, &registry.Chart{ID: "ipv4.packets", Family: "packets", Title: "IPv4 packets",
		Units: "packets/s", Priority: 3000, Module: mod, Dimensions: []*registry.Dimension{
			incDim("InReceives"), {ID: "OutRequests", Algorithm: registry.Incremental, Multiplier: -1},
			incDim("ForwDatagrams"), incDim("InDelivers"),
		}})
	f.ensureChart(reg, &registry.Chart{ID: "ipv4.errors", Family: "errors", Title: "IPv4 errors",
		Units: "packets/s", Priority: 3100, Module: mod, Dimensions: []*registry.Dimension{
			incDim("InDiscards"), incDim("OutDiscards"), incDim("InHdrErrors"), incDim("InAddrErrors"), incDim("OutNoRoutes"),
		}})
}

func (f *freebsdCollector) addIPv6Charts(reg *registry.Registry) {
	mod := "net.inet6.ip6.stats"
	f.ensureChart(reg, &registry.Chart{ID: "ipv6.packets", Family: "packets", Title: "IPv6 packets",
		Units: "packets/s", Priority: 3001, Module: mod, Dimensions: []*registry.Dimension{
			incDim("received"), {ID: "sent", Algorithm: registry.Incremental, Multiplier: -1},
			incDim("forwarded"), incDim("delivered"),
		}})
	f.ensureChart(reg, &registry.Chart{ID: "ipv6.errors", Family: "errors", Title: "IPv6 errors",
		Units: "packets/s", Priority: 3101, Module: mod, Dimensions: []*registry.Dimension{
			incDim("InDiscards"), incDim("OutDiscards"), incDim("InHdrErrors"), incDim("InAddrErrors"), incDim("OutNoRoutes"),
		}})
	f.ensureChart(reg, &registry.Chart{ID: "ipv6.icmp", Family: "icmp", Title: "IPv6 ICMP messages",
		Units: "messages/s", Priority: 2603, Module: "net.inet6.icmp6.stats",
		Dimensions: []*registry.Dimension{incDim("received"), incDim("sent")}})
}

func (f *freebsdCollector) addZFSChartsFBSD(reg *registry.Registry, m map[string]string) {
	if _, ok := reg.Chart("zfs.arc_size"); !ok {
		addZFSCharts(reg)
		for _, id := range []string{"zfs.arc_size", "zfs.hits_rate", "zfs.hits", "zfs.l2hits_rate", "zfs.reads"} {
			if ch, ok := reg.Chart(id); ok {
				ch.Plugin, ch.Module = "freebsd", "zfs"
			}
		}
	}
	f.ensureChart(reg, &registry.Chart{ID: "zfs.l2_size", Family: "zfs", Title: "ZFS L2 ARC size", Units: "MiB",
		Type: registry.Area, Priority: 2501, Module: "zfs", Dimensions: []*registry.Dimension{
			{ID: "actual", Divisor: 1 << 20}, {ID: "size", Divisor: 1 << 20},
		}})
	f.ensureChart(reg, &registry.Chart{ID: "zfs.bytes", Family: "zfs", Title: "ZFS ARC L2 read/write rate", Units: "KiB/s",
		Type: registry.Area, Priority: 2531, Module: "zfs", Dimensions: []*registry.Dimension{
			{ID: "read", Algorithm: registry.Incremental, Divisor: 1024},
			{ID: "write", Algorithm: registry.Incremental, Multiplier: -1, Divisor: 1024},
		}})
	f.ensureChart(reg, &registry.Chart{ID: "zfs.memory_ops", Family: "zfs", Title: "ZFS memory operations",
		Units: "operations/s", Priority: 2540, Module: "zfs", Dimensions: []*registry.Dimension{incDim("throttled")}})
	f.ensureChart(reg, &registry.Chart{ID: "zfs.important_ops", Family: "zfs", Title: "ZFS important operations",
		Units: "operations/s", Priority: 2541, Module: "zfs", Dimensions: []*registry.Dimension{
			incDim("evict_skip"), incDim("deleted"), incDim("mutex_miss"), incDim("hash_collisions"),
		}})
	f.ensureChart(reg, &registry.Chart{ID: "zfs.arc_size_breakdown", Family: "zfs", Title: "ZFS ARC size breakdown",
		Units: "percentage", Type: registry.Stacked, Priority: 2502, Module: "zfs", Dimensions: []*registry.Dimension{
			{ID: "recent", Algorithm: registry.PercentageOfAbsoluteRow},
			{ID: "frequent", Algorithm: registry.PercentageOfAbsoluteRow},
		}})
	if sysctlHas(m, "kstat.zfs.misc.zio_trim.bytes") || sysctlHasPrefix(m, "kstat.zfs.misc.zio_trim.") {
		f.ensureChart(reg, &registry.Chart{ID: "zfs.trim_bytes", Family: "trim", Title: "Successfully TRIMmed bytes",
			Units: "bytes", Priority: 2320, Module: "zfs", Dimensions: []*registry.Dimension{incDim("TRIMmed")}})
		f.ensureChart(reg, &registry.Chart{ID: "zfs.trim_requests", Family: "trim", Title: "TRIM requests",
			Units: "requests", Type: registry.Stacked, Priority: 2321, Module: "zfs", Dimensions: []*registry.Dimension{
				incDim("successful"), incDim("failed"), incDim("unsupported"),
			}})
	}
}

func (f *freebsdCollector) collectM22(ctx context.Context, reg *registry.Registry, now time.Time, m map[string]string) {
	page := sysctlUint(m, "hw.pagesize", "vm.stats.vm.v_page_size")
	if page == 0 {
		page = 4096
	}
	if _, ok := reg.Chart("system.syscalls"); ok {
		_ = reg.Collect("system.syscalls", now, map[string]float64{
			"calls": sysctlUint(m, "vm.stats.sys.v_syscall", "vm.stats.sys.v_syscalls"),
		})
	}
	if _, ok := reg.Chart("mem.pgfaults"); ok {
		_ = reg.Collect("mem.pgfaults", now, map[string]float64{
			"memory":        sysctlUint(m, "vm.stats.vm.v_vm_faults"),
			"io_requiring":  sysctlUint(m, "vm.stats.vm.v_io_faults"),
			"cow":           sysctlUint(m, "vm.stats.vm.v_cow_faults"),
			"cow_optimized": sysctlUint(m, "vm.stats.vm.v_cow_optim"),
			"in_transit":    sysctlUint(m, "vm.stats.vm.v_intrans"),
		})
	}
	if _, ok := reg.Chart("mem.swapio"); ok {
		_ = reg.Collect("mem.swapio", now, map[string]float64{
			"in":  sysctlUint(m, "vm.stats.vm.v_swappgsin") * page,
			"out": sysctlUint(m, "vm.stats.vm.v_swappgsout") * page,
		})
	}
	if _, ok := reg.Chart("system.ram"); ok {
		free := sysctlUint(m, "vm.stats.vm.v_free_count") * page
		active := sysctlUint(m, "vm.stats.vm.v_active_count") * page
		inactive := sysctlUint(m, "vm.stats.vm.v_inactive_count") * page
		wired := sysctlUint(m, "vm.stats.vm.v_wire_count") * page
		cache := sysctlUint(m, "vm.stats.vm.v_cache_count") * page
		laundry := sysctlUint(m, "vm.stats.vm.v_laundry_count") * page
		buf := sysctlUint(m, "vfs.bufspace")
		_ = reg.Collect("system.ram", now, map[string]float64{
			"free": free, "active": active, "inactive": inactive, "wired": wired,
			"cache": cache, "cached": cache, "laundry": laundry, "buffers": buf,
			"used": active + wired,
		})
	}
	if _, ok := reg.Chart("mem.available"); ok {
		avail := (sysctlUint(m, "vm.stats.vm.v_free_count") + sysctlUint(m, "vm.stats.vm.v_inactive_count") +
			sysctlUint(m, "vm.stats.vm.v_cache_count") + sysctlUint(m, "vm.stats.vm.v_laundry_count")) * page
		_ = reg.Collect("mem.available", now, map[string]float64{"avail": avail})
	}
	if _, ok := reg.Chart("system.active_processes"); ok {
		v := sysctlUint(m, "vm.vmtotal.t_rq")
		if v == 0 {
			v = sysctlUint(m, "vm.stats.vm.v_active_count")
		}
		_ = reg.Collect("system.active_processes", now, map[string]float64{"active": v})
	}
	if _, ok := reg.Chart("cpu.scaling_cur_freq"); ok {
		_ = reg.Collect("cpu.scaling_cur_freq", now, map[string]float64{"frequency": sysctlUint(m, "dev.cpu.0.freq")})
	}
	f.collectIntrcnt(reg, now, m)
	if _, ok := reg.Chart("system.softnet_stat"); ok {
		_ = reg.Collect("system.softnet_stat", now, map[string]float64{
			"dispatched":        sysctlUint(m, "net.isr.dispatched"),
			"hybrid_dispatched": sysctlUint(m, "net.isr.hybrid_dispatched"),
			"qdrops":            sysctlUint(m, "net.isr.qdrops"),
			"queued":            sysctlUint(m, "net.isr.queued"),
		})
	}
	f.collectTCPStates(reg, now, m)
	f.collectInetStats(reg, now, m)
	f.collectZFS(reg, now, m)
	f.collectIPFW(ctx, reg, now, m)
	f.collectGstat(ctx, reg, now)
	f.collectDF(ctx, reg, now)
	f.collectIfaddrs(ctx, reg, now)
}

func (f *freebsdCollector) collectIntrcnt(reg *registry.Registry, now time.Time, m map[string]string) {
	ch, ok := reg.Chart("system.interrupts")
	if !ok {
		return
	}
	names := strings.Fields(strings.Trim(m["hw.intrnames"], `"`))
	cnts := sysctlNums(m["hw.intrcnt"])
	vals := map[string]float64{}
	for i, n := range names {
		if i >= len(cnts) {
			break
		}
		id := sanitizeID(n)
		if id == "" {
			id = "irq" + strconv.Itoa(i)
		}
		if !f.irqSeen[id] {
			ch.AddDimension(incDim(id))
			f.irqSeen[id] = true
		}
		vals[id] = cnts[i]
	}
	if len(vals) > 0 {
		_ = reg.Collect("system.interrupts", now, vals)
	}
}

func (f *freebsdCollector) collectTCPStates(reg *registry.Registry, now time.Time, m map[string]string) {
	if _, ok := reg.Chart("ipv4.tcpsock"); !ok {
		return
	}
	nums := sysctlNums(m["net.inet.tcp.states"])
	estab := 0.0
	if len(nums) > 4 {
		estab = nums[4] // TCPS_ESTABLISHED
	}
	if estab == 0 {
		estab = sysctlUint(m, "net.inet.tcp.stats.tcps_estab", "tcps_estab")
	}
	_ = reg.Collect("ipv4.tcpsock", now, map[string]float64{"CurrEstab": estab})
}

func (f *freebsdCollector) collectInetStats(reg *registry.Registry, now time.Time, m map[string]string) {
	g := func(keys ...string) float64 { return sysctlUint(m, keys...) }
	if _, ok := reg.Chart("ipv4.tcppackets"); ok {
		_ = reg.Collect("ipv4.tcppackets", now, map[string]float64{
			"InSegs":  g("net.inet.tcp.stats.tcps_rcvtotal", "tcps_rcvtotal"),
			"OutSegs": g("net.inet.tcp.stats.tcps_sndtotal", "tcps_sndtotal"),
		})
	}
	if _, ok := reg.Chart("ipv4.tcperrors"); ok {
		inErr := g("net.inet.tcp.stats.tcps_rcvbadoff", "tcps_rcvbadoff") +
			g("net.inet.tcp.stats.tcps_rcvshort", "tcps_rcvshort") +
			g("net.inet.tcp.stats.tcps_rcvreassfull", "tcps_rcvreassfull")
		_ = reg.Collect("ipv4.tcperrors", now, map[string]float64{
			"InErrs":       inErr,
			"InCsumErrors": g("net.inet.tcp.stats.tcps_rcvbadsum", "tcps_rcvbadsum"),
			"RetransSegs":  g("net.inet.tcp.stats.tcps_sndrexmitpack", "tcps_sndrexmitpack"),
		})
	}
	if _, ok := reg.Chart("ipv4.tcphandshake"); ok {
		_ = reg.Collect("ipv4.tcphandshake", now, map[string]float64{
			"EstabResets":  g("net.inet.tcp.stats.tcps_drops", "tcps_drops"),
			"ActiveOpens":  g("net.inet.tcp.stats.tcps_connattempt", "tcps_connattempt"),
			"PassiveOpens": g("net.inet.tcp.stats.tcps_accepts", "tcps_accepts"),
			"AttemptFails": g("net.inet.tcp.stats.tcps_conndrops", "tcps_conndrops"),
		})
	}
	if _, ok := reg.Chart("ipv4.udppackets"); ok {
		_ = reg.Collect("ipv4.udppackets", now, map[string]float64{
			"InDatagrams":  g("net.inet.udp.stats.udps_ipackets", "udps_ipackets"),
			"OutDatagrams": g("net.inet.udp.stats.udps_opackets", "udps_opackets"),
		})
	}
	if _, ok := reg.Chart("ipv4.udperrors"); ok {
		_ = reg.Collect("ipv4.udperrors", now, map[string]float64{
			"InErrors":     g("net.inet.udp.stats.udps_hdrops", "udps_hdrops") + g("net.inet.udp.stats.udps_badlen", "udps_badlen"),
			"NoPorts":      g("net.inet.udp.stats.udps_noport", "udps_noport"),
			"RcvbufErrors": g("net.inet.udp.stats.udps_fullsock", "udps_fullsock"),
			"SndbufErrors": g("net.inet.udp.stats.udps_nosum", "udps_nosum"),
			"InCsumErrors": g("net.inet.udp.stats.udps_badsum", "udps_badsum"),
		})
	}
	if _, ok := reg.Chart("ipv4.icmp"); ok {
		_ = reg.Collect("ipv4.icmp", now, map[string]float64{
			"InMsgs":  g("net.inet.icmp.stats.icps_received", "icps_received") + g("net.inet.icmp.stats.icps_inhist", "icps_inhist"),
			"OutMsgs": g("net.inet.icmp.stats.icps_sent", "icps_sent") + g("net.inet.icmp.stats.icps_outhist", "icps_outhist"),
		})
	}
	if _, ok := reg.Chart("ipv4.icmp_errors"); ok {
		_ = reg.Collect("ipv4.icmp_errors", now, map[string]float64{
			"InErrors":  g("net.inet.icmp.stats.icps_badcode", "icps_badcode") + g("net.inet.icmp.stats.icps_tooshort", "icps_tooshort"),
			"OutErrors": g("net.inet.icmp.stats.icps_error", "icps_error"),
		})
	}
	if _, ok := reg.Chart("ipv4.packets"); ok {
		_ = reg.Collect("ipv4.packets", now, map[string]float64{
			"InReceives":    g("net.inet.ip.stats.ips_total", "ips_total"),
			"OutRequests":   g("net.inet.ip.stats.ips_localout", "ips_localout"),
			"ForwDatagrams": g("net.inet.ip.stats.ips_forward", "ips_forward"),
			"InDelivers":    g("net.inet.ip.stats.ips_delivered", "ips_delivered"),
		})
	}
	if _, ok := reg.Chart("ipv4.errors"); ok {
		_ = reg.Collect("ipv4.errors", now, map[string]float64{
			"InDiscards":   g("net.inet.ip.stats.ips_odropped", "ips_odropped"),
			"OutDiscards":  g("net.inet.ip.stats.ips_noroute", "ips_noroute"),
			"InHdrErrors":  g("net.inet.ip.stats.ips_badvers", "ips_badvers") + g("net.inet.ip.stats.ips_badhlen", "ips_badhlen"),
			"InAddrErrors": g("net.inet.ip.stats.ips_badaddr", "ips_badaddr"),
			"OutNoRoutes":  g("net.inet.ip.stats.ips_cantforward", "ips_cantforward"),
		})
	}
	if _, ok := reg.Chart("ipv6.packets"); ok {
		_ = reg.Collect("ipv6.packets", now, map[string]float64{
			"received":  g("net.inet6.ip6.stats.ip6s_total", "ip6s_total"),
			"sent":      g("net.inet6.ip6.stats.ip6s_localout", "ip6s_localout"),
			"forwarded": g("net.inet6.ip6.stats.ip6s_forward", "ip6s_forward"),
			"delivered": g("net.inet6.ip6.stats.ip6s_delivered", "ip6s_delivered"),
		})
	}
	if _, ok := reg.Chart("ipv6.errors"); ok {
		_ = reg.Collect("ipv6.errors", now, map[string]float64{
			"InDiscards":   g("net.inet6.ip6.stats.ip6s_odropped", "ip6s_odropped"),
			"OutDiscards":  g("net.inet6.ip6.stats.ip6s_noroute", "ip6s_noroute"),
			"InHdrErrors":  g("net.inet6.ip6.stats.ip6s_badvers", "ip6s_badvers"),
			"InAddrErrors": g("net.inet6.ip6.stats.ip6s_tooshort", "ip6s_tooshort"),
			"OutNoRoutes":  g("net.inet6.ip6.stats.ip6s_cantforward", "ip6s_cantforward"),
		})
	}
	if _, ok := reg.Chart("ipv6.icmp"); ok {
		_ = reg.Collect("ipv6.icmp", now, map[string]float64{
			"received": g("net.inet6.icmp6.stats.icp6s_inhist", "icp6s_received"),
			"sent":     g("net.inet6.icmp6.stats.icp6s_outhist", "icp6s_sent"),
		})
	}
}

func (f *freebsdCollector) collectZFS(reg *registry.Registry, now time.Time, m map[string]string) {
	ks := sysctlSubtree(m, "kstat.zfs.misc.arcstats")
	if len(ks) == 0 {
		return
	}
	if _, ok := reg.Chart("zfs.arc_size"); ok {
		collectZFS(reg, now, ks)
	}
	if _, ok := reg.Chart("zfs.l2_size"); ok {
		_ = reg.Collect("zfs.l2_size", now, map[string]float64{"actual": ks["l2_asize"], "size": ks["l2_size"]})
	}
	if _, ok := reg.Chart("zfs.bytes"); ok {
		_ = reg.Collect("zfs.bytes", now, map[string]float64{"read": ks["l2_read_bytes"], "write": ks["l2_write_bytes"]})
	}
	if _, ok := reg.Chart("zfs.memory_ops"); ok {
		_ = reg.Collect("zfs.memory_ops", now, map[string]float64{"throttled": ks["memory_throttle_count"]})
	}
	if _, ok := reg.Chart("zfs.important_ops"); ok {
		_ = reg.Collect("zfs.important_ops", now, map[string]float64{
			"evict_skip": ks["evict_skip"], "deleted": ks["deleted"],
			"mutex_miss": ks["mutex_miss"], "hash_collisions": ks["hash_collisions"],
		})
	}
	if _, ok := reg.Chart("zfs.arc_size_breakdown"); ok {
		_ = reg.Collect("zfs.arc_size_breakdown", now, map[string]float64{"recent": ks["mru_size"], "frequent": ks["mfu_size"]})
	}
	trim := sysctlSubtree(m, "kstat.zfs.misc.zio_trim")
	if _, ok := reg.Chart("zfs.trim_bytes"); ok {
		_ = reg.Collect("zfs.trim_bytes", now, map[string]float64{"TRIMmed": trim["bytes"]})
	}
	if _, ok := reg.Chart("zfs.trim_requests"); ok {
		_ = reg.Collect("zfs.trim_requests", now, map[string]float64{
			"successful": trim["success"], "failed": trim["failed"], "unsupported": trim["unsupported"],
		})
	}
}

func (f *freebsdCollector) collectIPFW(ctx context.Context, reg *registry.Registry, now time.Time, m map[string]string) {
	raw := f.ipfwList
	if raw == nil {
		cmd := f.cfg.IPFW
		if cmd == "" {
			cmd = "ipfw"
		}
		if _, err := exec.LookPath(cmd); err != nil && f.run == nil {
			if sysctlUint(m, "net.inet.ip.fw.dyn_count") == 0 && !sysctlHas(m, "net.inet.ip.fw.enable") {
				return
			}
		}
		run := f.runner()
		out, err := run(ctx, cmd, "-a", "list")
		if err != nil {
			out, err = run(ctx, cmd, "show")
		}
		if err != nil {
			if dyn := sysctlUint(m, "net.inet.ip.fw.dyn_count"); dyn > 0 {
				f.ensureIPFWCharts(reg)
				_ = reg.Collect("ipfw.active", now, map[string]float64{"dynamic": dyn})
			}
			return
		}
		raw = out
	}
	rules := parseIPFWList(string(raw))
	if len(rules) == 0 && sysctlUint(m, "net.inet.ip.fw.dyn_count") == 0 {
		return
	}
	f.ensureIPFWCharts(reg)
	pkts, bytes := map[string]float64{}, map[string]float64{}
	active, expired := map[string]float64{}, map[string]float64{}
	var statMem, dynMem float64
	chP, _ := reg.Chart("ipfw.packets")
	chB, _ := reg.Chart("ipfw.bytes")
	chA, _ := reg.Chart("ipfw.active")
	chE, _ := reg.Chart("ipfw.expired")
	for _, r := range rules {
		id := r.ID
		if chP != nil && !f.ipfwSeen["p:"+id] {
			chP.AddDimension(incDim(id))
			f.ipfwSeen["p:"+id] = true
		}
		if chB != nil && !f.ipfwSeen["b:"+id] {
			chB.AddDimension(incDim(id))
			f.ipfwSeen["b:"+id] = true
		}
		pkts[id] = r.Packets
		bytes[id] = r.Bytes
		statMem += 64
		if r.Dynamic {
			dynID := r.Rule
			if chA != nil && !f.ipfwSeen["a:"+dynID] {
				chA.AddDimension(&registry.Dimension{ID: dynID})
				f.ipfwSeen["a:"+dynID] = true
			}
			if chE != nil && !f.ipfwSeen["e:"+dynID] {
				chE.AddDimension(&registry.Dimension{ID: dynID})
				f.ipfwSeen["e:"+dynID] = true
			}
			if r.Expired {
				expired[dynID]++
			} else {
				active[dynID]++
			}
			dynMem += 128
		}
	}
	if dyn := sysctlUint(m, "net.inet.ip.fw.dyn_count"); dyn > 0 && len(active) == 0 {
		active["dynamic"] = dyn
		if chA != nil && !f.ipfwSeen["a:dynamic"] {
			chA.AddDimension(&registry.Dimension{ID: "dynamic"})
			f.ipfwSeen["a:dynamic"] = true
		}
	}
	if len(pkts) > 0 {
		_ = reg.Collect("ipfw.packets", now, pkts)
		_ = reg.Collect("ipfw.bytes", now, bytes)
	}
	if len(active) > 0 {
		_ = reg.Collect("ipfw.active", now, active)
	}
	if len(expired) > 0 {
		_ = reg.Collect("ipfw.expired", now, expired)
	}
	_ = reg.Collect("ipfw.mem", now, map[string]float64{"static": statMem, "dynamic": dynMem})
}

func (f *freebsdCollector) ensureIPFWCharts(reg *registry.Registry) {
	f.ensureChart(reg, &registry.Chart{ID: "ipfw.mem", Family: "memory allocated", Title: "Memory allocated by rules",
		Units: "bytes", Type: registry.Stacked, Priority: 50000, Module: "ipfw",
		Dimensions: []*registry.Dimension{{ID: "dynamic"}, {ID: "static"}}})
	f.ensureChart(reg, &registry.Chart{ID: "ipfw.packets", Family: "static rules", Title: "Packets",
		Units: "packets/s", Type: registry.Stacked, Priority: 50010, Module: "ipfw"})
	f.ensureChart(reg, &registry.Chart{ID: "ipfw.bytes", Family: "static rules", Title: "Bytes",
		Units: "bytes/s", Type: registry.Stacked, Priority: 50020, Module: "ipfw"})
	f.ensureChart(reg, &registry.Chart{ID: "ipfw.active", Family: "dynamic_rules", Title: "Active rules",
		Units: "rules", Type: registry.Stacked, Priority: 50030, Module: "ipfw"})
	f.ensureChart(reg, &registry.Chart{ID: "ipfw.expired", Family: "dynamic_rules", Title: "Expired rules",
		Units: "rules", Type: registry.Stacked, Priority: 50040, Module: "ipfw"})
}

func (f *freebsdCollector) collectGstat(ctx context.Context, reg *registry.Registry, now time.Time) {
	raw := f.gstatOut
	if raw == nil {
		cmd := f.cfg.Gstat
		if cmd == "" {
			cmd = "gstat"
		}
		run := f.runner()
		out, err := run(ctx, cmd, "-b")
		if err != nil {
			return
		}
		raw = out
	}
	disks := parseGstat(string(raw))
	if len(disks) == 0 {
		return
	}
	sysIn, sysOut := 0.0, 0.0
	wrote := false
	for _, d := range disks {
		id := sanitizeID(d.Name)
		if _, ok := reg.Chart("disk." + id); ok && !f.diskSeen[id] {
			continue
		}
		f.ensureDiskCharts(reg, d.Name, id)
		_ = reg.Collect("disk."+id, now, map[string]float64{"reads": d.ReadBytes, "writes": d.WriteBytes})
		_ = reg.Collect("disk_ops."+id, now, map[string]float64{"reads": d.Reads, "writes": d.Writes})
		_ = reg.Collect("disk_qops."+id, now, map[string]float64{"operations": d.Queue})
		_ = reg.Collect("disk_util."+id, now, map[string]float64{"utilization": d.Busy})
		sysIn += d.ReadBytes
		sysOut += d.WriteBytes
		wrote = true
	}
	if !wrote {
		return
	}
	f.ensureChart(reg, &registry.Chart{ID: "system.io", Family: "disk", Title: "Disk I/O", Units: "KiB/s",
		Type: registry.Area, Priority: 150, Module: "devstat", Dimensions: []*registry.Dimension{
			{ID: "in", Algorithm: registry.Incremental, Divisor: 1024},
			{ID: "out", Algorithm: registry.Incremental, Multiplier: -1, Divisor: 1024},
		}})
	_ = reg.Collect("system.io", now, map[string]float64{"in": sysIn, "out": sysOut})
}

func (f *freebsdCollector) ensureDiskCharts(reg *registry.Registry, name, id string) {
	if f.diskSeen[id] {
		return
	}
	f.diskSeen[id] = true
	lbl := map[string]string{"device": name}
	f.ensureChart(reg, &registry.Chart{ID: "disk." + id, Context: "disk.io", Family: name, Title: "Disk I/O bandwidth",
		Units: "KiB/s", Type: registry.Area, Priority: 2000, Module: "devstat", Labels: lbl,
		Dimensions: []*registry.Dimension{
			{ID: "reads", Algorithm: registry.Incremental, Divisor: 1024},
			{ID: "writes", Algorithm: registry.Incremental, Multiplier: -1, Divisor: 1024},
		}})
	f.ensureChart(reg, &registry.Chart{ID: "disk_ops." + id, Context: "disk.ops", Family: name, Title: "Disk completed I/O operations",
		Units: "operations/s", Priority: 2001, Module: "devstat", Labels: lbl,
		Dimensions: []*registry.Dimension{incDim("reads"), incDim("writes")}})
	f.ensureChart(reg, &registry.Chart{ID: "disk_qops." + id, Context: "disk.qops", Family: name, Title: "Disk current I/O operations",
		Units: "operations", Priority: 2002, Module: "devstat", Labels: lbl,
		Dimensions: []*registry.Dimension{{ID: "operations"}}})
	f.ensureChart(reg, &registry.Chart{ID: "disk_util." + id, Context: "disk.util", Family: name, Title: "Disk utilization time",
		Units: "% of time working", Type: registry.Area, Priority: 2003, Module: "devstat", Labels: lbl,
		Dimensions: []*registry.Dimension{{ID: "utilization"}}})
}

func (f *freebsdCollector) collectDF(ctx context.Context, reg *registry.Registry, now time.Time) {
	raw := f.dfOut
	if raw == nil {
		cmd := f.cfg.DF
		if cmd == "" {
			cmd = "df"
		}
		run := f.runner()
		out, err := run(ctx, cmd, "-kP")
		if err != nil {
			out, err = run(ctx, cmd, "-k")
		}
		if err != nil {
			return
		}
		raw = out
	}
	for _, row := range parseDF(string(raw)) {
		id := sanitizeID(row.Mount)
		if _, ok := reg.Chart("disk_space." + id); ok && !f.mntSeen[id] {
			continue
		}
		if !f.mntSeen[id] {
			f.mntSeen[id] = true
			lbl := map[string]string{"mount_point": row.Mount, "filesystem": row.FS}
			f.ensureChart(reg, &registry.Chart{ID: "disk_space." + id, Context: "disk.space", Family: row.Mount,
				Title: "Disk space usage", Units: "GiB", Type: registry.Stacked, Priority: 2023, Module: "getmntinfo",
				Labels: lbl, Dimensions: []*registry.Dimension{
					{ID: "avail", Divisor: 1024 * 1024}, {ID: "used", Divisor: 1024 * 1024}, {ID: "reserved", Divisor: 1024 * 1024},
				}})
			f.ensureChart(reg, &registry.Chart{ID: "disk_inodes." + id, Context: "disk.inodes", Family: row.Mount,
				Title: "Disk files (inodes) usage", Units: "inodes", Type: registry.Stacked, Priority: 2024, Module: "getmntinfo",
				Labels: lbl, Dimensions: []*registry.Dimension{{ID: "avail"}, {ID: "used"}, {ID: "reserved"}}})
		}
		_ = reg.Collect("disk_space."+id, now, map[string]float64{"avail": row.AvailKB, "used": row.UsedKB, "reserved": 0})
		_ = reg.Collect("disk_inodes."+id, now, map[string]float64{"avail": row.IAvail, "used": row.IUsed, "reserved": 0})
	}
}

func (f *freebsdCollector) collectIfaddrs(ctx context.Context, reg *registry.Registry, now time.Time) {
	raw := f.netstatOut
	if raw == nil {
		cmd := f.cfg.Netstat
		if cmd == "" {
			cmd = "netstat"
		}
		run := f.runner()
		out, err := run(ctx, cmd, "-ibn")
		if err != nil {
			return
		}
		raw = out
	}
	var totIn, totOut float64
	for _, iff := range parseNetstatIBN(string(raw)) {
		id := sanitizeID(iff.Name)
		if _, ok := reg.Chart("net." + id); ok && !f.ifSeen[id] {
			continue
		}
		if !f.ifSeen[id] {
			f.ifSeen[id] = true
			lbl := map[string]string{"device": iff.Name}
			f.ensureChart(reg, &registry.Chart{ID: "net." + id, Context: "net.net", Family: iff.Name, Title: "Bandwidth",
				Units: "kilobits/s", Type: registry.Area, Priority: 7000, Module: "getifaddrs", Labels: lbl,
				Dimensions: []*registry.Dimension{
					{ID: "received", Algorithm: registry.Incremental, Divisor: 1000 / 8},
					{ID: "sent", Algorithm: registry.Incremental, Multiplier: -1, Divisor: 1000 / 8},
				}})
			f.ensureChart(reg, &registry.Chart{ID: "net_packets." + id, Context: "net.packets", Family: iff.Name, Title: "Packets",
				Units: "packets/s", Priority: 7001, Module: "getifaddrs", Labels: lbl,
				Dimensions: []*registry.Dimension{incDim("received"), incDim("sent")}})
			f.ensureChart(reg, &registry.Chart{ID: "net_errors." + id, Context: "net.errors", Family: iff.Name, Title: "Interface errors",
				Units: "errors/s", Priority: 7002, Module: "getifaddrs", Labels: lbl,
				Dimensions: []*registry.Dimension{incDim("inbound"), incDim("outbound")}})
			f.ensureChart(reg, &registry.Chart{ID: "net_drops." + id, Context: "net.drops", Family: iff.Name, Title: "Interface drops",
				Units: "drops/s", Priority: 7003, Module: "getifaddrs", Labels: lbl,
				Dimensions: []*registry.Dimension{incDim("inbound"), incDim("outbound")}})
			f.ensureChart(reg, &registry.Chart{ID: "net_events." + id, Context: "net.events", Family: iff.Name, Title: "Network interface events",
				Units: "events/s", Priority: 7004, Module: "getifaddrs", Labels: lbl,
				Dimensions: []*registry.Dimension{incDim("collisions")}})
		}
		_ = reg.Collect("net."+id, now, map[string]float64{"received": iff.Ibytes, "sent": iff.Obytes})
		_ = reg.Collect("net_packets."+id, now, map[string]float64{"received": iff.Ipkts, "sent": iff.Opkts})
		_ = reg.Collect("net_errors."+id, now, map[string]float64{"inbound": iff.Ierrs, "outbound": iff.Oerrs})
		_ = reg.Collect("net_drops."+id, now, map[string]float64{"inbound": iff.Idrop, "outbound": 0})
		_ = reg.Collect("net_events."+id, now, map[string]float64{"collisions": iff.Coll})
		totIn += iff.Ibytes
		totOut += iff.Obytes
	}
	if totIn+totOut == 0 {
		return
	}
	f.ensureChart(reg, &registry.Chart{ID: "system.net", Family: "network", Title: "Network traffic",
		Units: "kilobits/s", Type: registry.Area, Priority: 500, Module: "getifaddrs", Dimensions: []*registry.Dimension{
			{ID: "InOctets", Name: "received", Algorithm: registry.Incremental, Divisor: 1000 / 8},
			{ID: "OutOctets", Name: "sent", Algorithm: registry.Incremental, Multiplier: -1, Divisor: 1000 / 8},
		}})
	_ = reg.Collect("system.net", now, map[string]float64{"InOctets": totIn, "OutOctets": totOut})
}

func (f *freebsdCollector) ensureChart(reg *registry.Registry, ch *registry.Chart) *registry.Chart {
	if existing, ok := reg.Chart(ch.ID); ok {
		return existing
	}
	if ch.Plugin == "" {
		ch.Plugin = "freebsd"
	}
	if ch.Module == "" {
		ch.Module = "sysctl"
	}
	reg.AddChart(ch)
	return ch
}

func sysctlHasPrefix(m map[string]string, prefix string) bool {
	for k := range m {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

func sysctlSubtree(m map[string]string, prefix string) map[string]float64 {
	out := map[string]float64{}
	p := prefix + "."
	for k, v := range m {
		if rest, ok := strings.CutPrefix(k, p); ok {
			out[rest] = firstFloat(v)
		} else if k == prefix {
			out["value"] = firstFloat(v)
		}
	}
	return out
}

func sysctlNums(s string) []float64 {
	s = strings.TrimSpace(strings.Trim(s, "{}[]\""))
	if s == "" {
		return nil
	}
	var out []float64
	for _, f := range strings.Fields(strings.ReplaceAll(s, ",", " ")) {
		out = append(out, firstFloat(f))
	}
	return out
}

type ipfwRule struct {
	ID, Rule         string
	Packets, Bytes   float64
	Dynamic, Expired bool
}

func parseIPFWList(s string) []ipfwRule {
	var out []ipfwRule
	seen := map[string]int{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		ruleNum := strings.TrimLeft(f[0], "0")
		if ruleNum == "" {
			ruleNum = "0"
		}
		if _, err := strconv.Atoi(ruleNum); err != nil {
			continue
		}
		pkts, bytes := firstFloat(f[1]), firstFloat(f[2])
		id := ruleNum
		if n := seen[ruleNum]; n > 0 {
			id = ruleNum + "_" + strconv.Itoa(n)
		}
		seen[ruleNum]++
		dyn := strings.Contains(strings.ToLower(line), "dyn") || strings.Contains(line, "##")
		exp := strings.Contains(strings.ToLower(line), "expired")
		out = append(out, ipfwRule{ID: id, Rule: ruleNum, Packets: pkts, Bytes: bytes, Dynamic: dyn, Expired: exp})
	}
	return out
}

type gstatRow struct {
	Name                       string
	ReadBytes, WriteBytes      float64
	Reads, Writes, Queue, Busy float64
}

func parseGstat(s string) []gstatRow {
	var out []gstatRow
	sc := bufio.NewScanner(strings.NewReader(s))
	var cols []string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "dT:") {
			continue
		}
		low := strings.ToLower(line)
		if strings.Contains(low, "name") && (strings.Contains(low, "busy") || strings.Contains(low, "ops")) {
			cols = strings.Fields(low)
			continue
		}
		if strings.HasPrefix(line, "#") {
			f := strings.Fields(strings.TrimPrefix(line, "#"))
			if len(f) >= 3 && strings.EqualFold(f[0], "name") {
				continue
			}
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		name := fields[len(fields)-1]
		if strings.EqualFold(name, "Name") {
			continue
		}
		row := gstatRow{Name: name}
		if len(cols) > 0 {
			// map by header: L(q) ops/s r/s kBps ms/r w/s kBps ms/w %busy Name
			idx := map[string]int{}
			for i, c := range cols {
				idx[c] = i
			}
			get := func(keys ...string) float64 {
				for _, k := range keys {
					if i, ok := idx[k]; ok && i < len(fields)-1 {
						return firstFloat(fields[i])
					}
				}
				return 0
			}
			row.Queue = get("l(q)", "q")
			row.Reads = get("r/s")
			row.Writes = get("w/s")
			// two kBps columns: first after r/s is read, first after w/s is write
			row.Busy = get("%busy", "busy")
			rkB, wkB := 0.0, 0.0
			seenKB := 0
			for i, c := range cols {
				if i >= len(fields)-1 {
					break
				}
				if c == "kbps" || c == "kBps" || c == "kb/s" {
					if seenKB == 0 {
						rkB = firstFloat(fields[i])
					} else if seenKB == 1 {
						wkB = firstFloat(fields[i])
					}
					seenKB++
				}
			}
			row.ReadBytes = rkB * 1024
			row.WriteBytes = wkB * 1024
		} else {
			// fixture: name rbytes wbytes reads writes qops busy
			if len(fields) >= 7 && fields[0] != "" && firstFloat(fields[1]) >= 0 {
				row = gstatRow{
					Name: fields[0], ReadBytes: firstFloat(fields[1]), WriteBytes: firstFloat(fields[2]),
					Reads: firstFloat(fields[3]), Writes: firstFloat(fields[4]),
					Queue: firstFloat(fields[5]), Busy: firstFloat(fields[6]),
				}
			} else {
				continue
			}
		}
		if row.Name == "" || strings.Contains(row.Name, "/") && strings.HasPrefix(row.Name, "dT") {
			continue
		}
		out = append(out, row)
	}
	return out
}

type dfRow struct {
	FS, Mount       string
	UsedKB, AvailKB float64
	IUsed, IAvail   float64
}

func parseDF(s string) []dfRow {
	var out []dfRow
	sc := bufio.NewScanner(strings.NewReader(s))
	header := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if header && (strings.Contains(strings.ToLower(line), "filesystem") || strings.Contains(strings.ToLower(line), "mounted")) {
			header = false
			continue
		}
		header = false
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		mount := f[len(f)-1]
		if mount == "on" {
			continue
		}
		out = append(out, dfRow{
			FS: f[0], UsedKB: firstFloat(f[2]), AvailKB: firstFloat(f[3]),
			Mount: mount, IUsed: firstFloat(f[2]), IAvail: firstFloat(f[3]),
		})
	}
	return out
}

type ifRow struct {
	Name                        string
	Ipkts, Ierrs, Idrop, Ibytes float64
	Opkts, Oerrs, Obytes, Coll  float64
}

func parseNetstatIBN(s string) []ifRow {
	var out []ifRow
	sc := bufio.NewScanner(strings.NewReader(s))
	header := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if header && strings.Contains(line, "Name") && strings.Contains(line, "Mtu") {
			header = false
			continue
		}
		if !strings.Contains(line, "<Link") && !strings.Contains(line, "Link#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		// Name Mtu Network Address Ipkts Ierrs Idrop Ibytes Opkts Oerrs Obytes Coll
		// Address may be missing → fields shift. Find numeric run at the end.
		coll := firstFloat(f[len(f)-1])
		obytes := firstFloat(f[len(f)-2])
		oerrs := firstFloat(f[len(f)-3])
		opkts := firstFloat(f[len(f)-4])
		ibytes := firstFloat(f[len(f)-5])
		idrop := firstFloat(f[len(f)-6])
		ierrs := firstFloat(f[len(f)-7])
		ipkts := firstFloat(f[len(f)-8])
		out = append(out, ifRow{
			Name: f[0], Ipkts: ipkts, Ierrs: ierrs, Idrop: idrop, Ibytes: ibytes,
			Opkts: opkts, Oerrs: oerrs, Obytes: obytes, Coll: coll,
		})
	}
	return out
}
