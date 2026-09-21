package collect

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// procM8 holds Linux proc.plugin leftovers: IPv6, IPVS, NFS, ZFS ARC, Btrfs,
// wireless, KSM and zram. Paths are overridable in tests.
type procM8 struct {
	haveIPv6, haveSock6, haveIPVS, haveNFS, haveNFSD                   bool
	haveZFS, haveBtrfs, haveWireless, haveKSM, haveZram                bool
	snmp6, sockstat6, ipvs, nfs, nfsd, arc, btrfs, wireless, ksm, zram string
	wifiSeen, zramSeen, btrfsSeen                                      map[string]bool
}

func (m procM8) any() bool {
	return m.haveIPv6 || m.haveSock6 || m.haveIPVS || m.haveNFS || m.haveNFSD ||
		m.haveZFS || m.haveBtrfs || m.haveWireless || m.haveKSM || m.haveZram
}

func (p *procCollector) initM8(reg *registry.Registry) {
	m := &p.m8
	m.snmp6 = firstNonEmpty(m.snmp6, "/proc/net/snmp6")
	m.sockstat6 = firstNonEmpty(m.sockstat6, "/proc/net/sockstat6")
	m.ipvs = firstNonEmpty(m.ipvs, "/proc/net/ip_vs_stats")
	m.nfs = firstNonEmpty(m.nfs, "/proc/net/rpc/nfs")
	m.nfsd = firstNonEmpty(m.nfsd, "/proc/net/rpc/nfsd")
	m.arc = firstNonEmpty(m.arc, "/proc/spl/kstat/zfs/arcstats")
	m.btrfs = firstNonEmpty(m.btrfs, "/sys/fs/btrfs")
	m.wireless = firstNonEmpty(m.wireless, "/proc/net/wireless")
	m.ksm = firstNonEmpty(m.ksm, "/sys/kernel/mm/ksm")
	m.zram = firstNonEmpty(m.zram, "/sys/block")
	m.wifiSeen, m.zramSeen, m.btrfsSeen = map[string]bool{}, map[string]bool{}, map[string]bool{}

	if raw, err := readTrim(m.snmp6); err == nil && strings.Contains(raw, "Ip6InReceives") {
		m.haveIPv6 = true
		addIPv6Charts(reg)
	}
	if raw, err := readTrim(m.sockstat6); err == nil && strings.Contains(raw, "inuse") {
		m.haveSock6 = true
		reg.AddChart(ip6Chart("ipv6.sockstat6_tcp_sockets", "IPv6 TCP sockets", "sockets", 3500, &registry.Dimension{ID: "inuse"}))
		reg.AddChart(ip6Chart("ipv6.sockstat6_udp_sockets", "IPv6 UDP sockets", "sockets", 3501, &registry.Dimension{ID: "inuse"}))
	}
	if _, err := readTrim(m.ipvs); err == nil {
		m.haveIPVS = true
	} else if _, err := readTrim("/proc/net/ip_vs/stats"); err == nil {
		m.ipvs = "/proc/net/ip_vs/stats"
		m.haveIPVS = true
	}
	if m.haveIPVS {
		ipvs := func(id, title, units string, prio int, dims ...*registry.Dimension) {
			ch := sysChart(id, "ipvs", title, units, prio, dims...)
			ch.Plugin, ch.Module = "proc", "ipvs"
			reg.AddChart(ch)
		}
		ipvs("ipvs.sockets", "IPVS new connections", "connections/s", 3200, incDim("connections"))
		ipvs("ipvs.packets", "IPVS packets", "packets/s", 3210, incDim("received"),
			&registry.Dimension{ID: "sent", Algorithm: registry.Incremental, Multiplier: -1})
		ch := sysChart("ipvs.net", "ipvs", "IPVS bandwidth", "kilobits/s", 3220,
			&registry.Dimension{ID: "received", Algorithm: registry.Incremental, Multiplier: 8, Divisor: 1000},
			&registry.Dimension{ID: "sent", Algorithm: registry.Incremental, Multiplier: -8, Divisor: 1000})
		ch.Plugin, ch.Module, ch.Type = "proc", "ipvs", registry.Area
		reg.AddChart(ch)
	}
	if raw, err := readTrim(m.nfs); err == nil && strings.Contains(raw, "rpc") {
		m.haveNFS = true
		addNFSCharts(reg, "nfs", "NFS client")
	}
	if raw, err := readTrim(m.nfsd); err == nil && strings.Contains(raw, "rpc") {
		m.haveNFSD = true
		addNFSCharts(reg, "nfsd", "NFS server")
		ch := sysChart("nfsd.io", "nfsd", "NFS server I/O", "kilobytes/s", 3310,
			incDim("read"), &registry.Dimension{ID: "write", Algorithm: registry.Incremental, Multiplier: -1})
		ch.Plugin, ch.Module = "proc", "nfsd"
		reg.AddChart(ch)
		ch = sysChart("nfsd.reply_cache", "nfsd", "NFS server reply cache", "hits/s", 3311,
			incDim("hits"), incDim("misses"), incDim("nocache"))
		ch.Plugin, ch.Module = "proc", "nfsd"
		reg.AddChart(ch)
	}
	if raw, err := readTrim(m.arc); err == nil && strings.Contains(raw, "hits") {
		m.haveZFS = true
		addZFSCharts(reg)
	}
	if ents, err := os.ReadDir(m.btrfs); err == nil {
		for _, e := range ents {
			if e.Name() != "features" && (e.IsDir() || e.Type()&os.ModeSymlink != 0) {
				m.haveBtrfs = true
				break
			}
		}
	}
	if raw, err := readTrim(m.wireless); err == nil && strings.Contains(raw, "face") {
		m.haveWireless = true
	}
	if _, err := readTrim(filepath.Join(m.ksm, "pages_shared")); err == nil {
		m.haveKSM = true
		reg.AddChart(chartMeta(&registry.Chart{ID: "mem.ksm", Family: "ksm", Title: "Kernel Same Page Merging",
			Units: "MiB", Priority: 1020, Type: registry.Stacked, Plugin: "proc", Module: "ksm",
			Dimensions: []*registry.Dimension{
				{ID: "shared", Divisor: 1 << 20},
				{ID: "unshared", Multiplier: -1, Divisor: 1 << 20},
				{ID: "sharing", Divisor: 1 << 20},
				{ID: "volatile", Multiplier: -1, Divisor: 1 << 20},
			}}, "ksm", "proc", "ksm"))
		reg.AddChart(chartMeta(&registry.Chart{ID: "mem.ksm_savings", Family: "ksm", Title: "Kernel Same Page Merging Savings",
			Units: "MiB", Priority: 1021, Type: registry.Area, Plugin: "proc", Module: "ksm",
			Dimensions: []*registry.Dimension{
				{ID: "savings", Multiplier: -1, Divisor: 1 << 20},
				{ID: "offered", Divisor: 1 << 20},
			}}, "ksm", "proc", "ksm"))
		reg.AddChart(chartMeta(&registry.Chart{ID: "mem.ksm_ratios", Family: "ksm", Title: "Kernel Same Page Merging Effectiveness",
			Units: "percentage", Priority: 1022, Plugin: "proc", Module: "ksm",
			Dimensions: []*registry.Dimension{{ID: "savings"}}}, "ksm", "proc", "ksm"))
	}
	if ents, err := os.ReadDir(m.zram); err == nil {
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), "zram") {
				m.haveZram = true
				break
			}
		}
	}
}

func ip6Chart(id, title, units string, prio int, dims ...*registry.Dimension) *registry.Chart {
	ch := sysChart(id, "ipv6", title, units, prio, dims...)
	ch.Plugin, ch.Module = "proc", "snmp6"
	return ch
}

func addIPv6Charts(reg *registry.Registry) {
	reg.AddChart(ip6Chart("ipv6.packets", "IPv6 packets", "packets/s", 3450, incDim("received"),
		&registry.Dimension{ID: "sent", Algorithm: registry.Incremental, Multiplier: -1},
		incDim("forwarded"), incDim("delivered")))
	reg.AddChart(ip6Chart("ipv6.errors", "IPv6 errors", "packets/s", 3451,
		incDim("InDiscards"), incDim("OutDiscards"), incDim("InHdrErrors"),
		incDim("InAddrErrors"), incDim("OutNoRoutes"), incDim("InTruncatedPkts"),
		incDim("InNoRoutes"), incDim("InUnknownProtos"), incDim("InTooBigErrors")))
	reg.AddChart(ip6Chart("ipv6.udppackets", "IPv6 UDP packets", "packets/s", 3460, incDim("received"),
		&registry.Dimension{ID: "sent", Algorithm: registry.Incremental, Multiplier: -1}))
	reg.AddChart(ip6Chart("ipv6.udperrors", "IPv6 UDP errors", "events/s", 3461,
		incDim("InErrors"), incDim("NoPorts"), incDim("RcvbufErrors"), incDim("SndbufErrors"), incDim("InCsumErrors")))
	reg.AddChart(ip6Chart("ipv6.icmp", "IPv6 ICMP messages", "messages/s", 3470, incDim("received"),
		&registry.Dimension{ID: "sent", Algorithm: registry.Incremental, Multiplier: -1}))
	reg.AddChart(ip6Chart("ipv6.icmpechos", "IPv6 ICMP echoes", "messages/s", 3471,
		incDim("InEchos"), incDim("OutEchos"), incDim("InEchoReplies"), incDim("OutEchoReplies")))
}

func addNFSCharts(reg *registry.Registry, prefix, title string) {
	rpc := sysChart(prefix+".rpc", prefix, title+" RPC", "calls/s", 3300, incDim("calls"), incDim("retransmits"), incDim("authrefresh"))
	rpc.Plugin, rpc.Module = "proc", prefix
	reg.AddChart(rpc)
	proc := sysChart(prefix+".proc3", prefix, title+" NFSv3 procedures", "calls/s", 3301,
		incDim("getattr"), incDim("lookup"), incDim("access"), incDim("read"), incDim("write"),
		incDim("create"), incDim("remove"), incDim("readdir"), incDim("commit"), incDim("other"))
	proc.Plugin, proc.Module = "proc", prefix
	reg.AddChart(proc)
}

func addZFSCharts(reg *registry.Registry) {
	z := func(id, title, units string, prio int, typ registry.ChartType, dims ...*registry.Dimension) {
		ch := sysChart(id, "zfs", title, units, prio, dims...)
		ch.Plugin, ch.Module, ch.Type = "proc", "zfs", typ
		reg.AddChart(ch)
	}
	z("zfs.arc_size", "ZFS ARC size", "MiB", 2500, registry.Area,
		&registry.Dimension{ID: "arcsz", Divisor: 1 << 20},
		&registry.Dimension{ID: "target", Divisor: 1 << 20},
		&registry.Dimension{ID: "min", Divisor: 1 << 20},
		&registry.Dimension{ID: "max", Divisor: 1 << 20})
	z("zfs.hits_rate", "ZFS ARC hits rate", "events/s", 2510, registry.Stacked, incDim("hits"), incDim("misses"))
	z("zfs.hits", "ZFS ARC hits", "percentage", 2511, registry.Stacked,
		&registry.Dimension{ID: "hits", Algorithm: registry.PercentageOfAbsoluteRow},
		&registry.Dimension{ID: "misses", Algorithm: registry.PercentageOfAbsoluteRow})
	z("zfs.l2hits_rate", "ZFS L2 ARC hits rate", "events/s", 2520, registry.Stacked, incDim("hits"), incDim("misses"))
	z("zfs.reads", "ZFS reads", "reads/s", 2530, registry.Line, incDim("arc"), incDim("demand"), incDim("prefetch"), incDim("l2"))
}

func (p *procCollector) collectM8(reg *registry.Registry, now time.Time) {
	m := &p.m8
	if m.haveIPv6 {
		if raw, err := readTrim(m.snmp6); err == nil {
			collectIPv6(reg, now, parseKVFloat(raw))
		}
	}
	if m.haveSock6 {
		if raw, err := readTrim(m.sockstat6); err == nil {
			st := parseSockstat6(raw)
			_ = reg.Collect("ipv6.sockstat6_tcp_sockets", now, map[string]float64{"inuse": st["tcp6_inuse"]})
			_ = reg.Collect("ipv6.sockstat6_udp_sockets", now, map[string]float64{"inuse": st["udp6_inuse"]})
		}
	}
	if m.haveIPVS {
		if raw, err := readTrim(m.ipvs); err == nil {
			if st, ok := parseIPVS(raw); ok {
				_ = reg.Collect("ipvs.sockets", now, map[string]float64{"connections": st.conns})
				_ = reg.Collect("ipvs.packets", now, map[string]float64{"received": st.inPkts, "sent": st.outPkts})
				_ = reg.Collect("ipvs.net", now, map[string]float64{"received": st.inBytes, "sent": st.outBytes})
			}
		}
	}
	if m.haveNFS {
		if raw, err := readTrim(m.nfs); err == nil {
			collectNFS(reg, now, "nfs", parseRPCStats(raw))
		}
	}
	if m.haveNFSD {
		if raw, err := readTrim(m.nfsd); err == nil {
			st := parseRPCStats(raw)
			collectNFS(reg, now, "nfsd", st)
			_ = reg.Collect("nfsd.io", now, map[string]float64{"read": st["io_read"], "write": st["io_write"]})
			_ = reg.Collect("nfsd.reply_cache", now, map[string]float64{"hits": st["rc_hits"], "misses": st["rc_misses"], "nocache": st["rc_nocache"]})
		}
	}
	if m.haveZFS {
		if raw, err := readTrim(m.arc); err == nil {
			collectZFS(reg, now, parseKstat(raw))
		}
	}
	if m.haveBtrfs {
		p.collectBtrfs(reg, now)
	}
	if m.haveWireless {
		if raw, err := readTrim(m.wireless); err == nil {
			p.collectWireless(reg, now, parseWireless(raw))
		}
	}
	if m.haveKSM {
		p.collectKSM(reg, now)
	}
	if m.haveZram {
		p.collectZram(reg, now)
	}
}

func collectIPv6(reg *registry.Registry, now time.Time, m map[string]float64) {
	g := func(k string) float64 { return m[k] }
	_ = reg.Collect("ipv6.packets", now, map[string]float64{
		"received": g("Ip6InReceives"), "sent": g("Ip6OutRequests"),
		"forwarded": g("Ip6OutForwDatagrams"), "delivered": g("Ip6InDelivers")})
	_ = reg.Collect("ipv6.errors", now, map[string]float64{
		"InDiscards": g("Ip6InDiscards"), "OutDiscards": g("Ip6OutDiscards"),
		"InHdrErrors": g("Ip6InHdrErrors"), "InAddrErrors": g("Ip6InAddrErrors"),
		"OutNoRoutes": g("Ip6OutNoRoutes"), "InTruncatedPkts": g("Ip6InTruncatedPkts"),
		"InNoRoutes": g("Ip6InNoRoutes"), "InUnknownProtos": g("Ip6InUnknownProtos"),
		"InTooBigErrors": g("Ip6InTooBigErrors")})
	_ = reg.Collect("ipv6.udppackets", now, map[string]float64{
		"received": g("Udp6InDatagrams"), "sent": g("Udp6OutDatagrams")})
	_ = reg.Collect("ipv6.udperrors", now, map[string]float64{
		"InErrors": g("Udp6InErrors"), "NoPorts": g("Udp6NoPorts"),
		"RcvbufErrors": g("Udp6RcvbufErrors"), "SndbufErrors": g("Udp6SndbufErrors"),
		"InCsumErrors": g("Udp6InCsumErrors")})
	_ = reg.Collect("ipv6.icmp", now, map[string]float64{"received": g("Icmp6InMsgs"), "sent": g("Icmp6OutMsgs")})
	_ = reg.Collect("ipv6.icmpechos", now, map[string]float64{
		"InEchos": g("Icmp6InEchos"), "OutEchos": g("Icmp6OutEchos"),
		"InEchoReplies": g("Icmp6InEchoReplies"), "OutEchoReplies": g("Icmp6OutEchoReplies")})
}

func collectNFS(reg *registry.Registry, now time.Time, prefix string, st map[string]float64) {
	_ = reg.Collect(prefix+".rpc", now, map[string]float64{
		"calls": st["rpc_calls"], "retransmits": st["rpc_retrans"], "authrefresh": st["rpc_authrefresh"]})
	named := []string{"getattr", "lookup", "access", "read", "write", "create", "remove", "readdir", "commit"}
	vals := map[string]float64{}
	var other float64
	seen := map[string]bool{}
	for _, n := range named {
		vals[n] = st["proc3_"+n]
		seen[n] = true
	}
	for k, v := range st {
		if strings.HasPrefix(k, "proc3_") && !seen[strings.TrimPrefix(k, "proc3_")] {
			other += v
		}
	}
	vals["other"] = other
	_ = reg.Collect(prefix+".proc3", now, vals)
}

func collectZFS(reg *registry.Registry, now time.Time, m map[string]float64) {
	_ = reg.Collect("zfs.arc_size", now, map[string]float64{
		"arcsz": m["size"], "target": m["c"], "min": m["c_min"], "max": m["c_max"]})
	hits, misses := m["hits"], m["misses"]
	_ = reg.Collect("zfs.hits_rate", now, map[string]float64{"hits": hits, "misses": misses})
	_ = reg.Collect("zfs.hits", now, map[string]float64{"hits": hits, "misses": misses})
	_ = reg.Collect("zfs.l2hits_rate", now, map[string]float64{"hits": m["l2_hits"], "misses": m["l2_misses"]})
	_ = reg.Collect("zfs.reads", now, map[string]float64{
		"arc": hits + misses, "demand": m["demand_data_hits"] + m["demand_data_misses"],
		"prefetch": m["prefetch_data_hits"] + m["prefetch_data_misses"], "l2": m["l2_hits"] + m["l2_misses"]})
}

func (p *procCollector) collectKSM(reg *registry.Registry, now time.Time) {
	page := float64(os.Getpagesize())
	if page <= 0 {
		page = 4096
	}
	shared, _ := readFloat(filepath.Join(p.m8.ksm, "pages_shared"))
	sharing, _ := readFloat(filepath.Join(p.m8.ksm, "pages_sharing"))
	unshared, _ := readFloat(filepath.Join(p.m8.ksm, "pages_unshared"))
	volatile, _ := readFloat(filepath.Join(p.m8.ksm, "pages_volatile"))
	_ = reg.Collect("mem.ksm", now, map[string]float64{
		"shared": shared * page, "unshared": unshared * page, "sharing": sharing * page, "volatile": volatile * page})
	offered := (sharing + shared + unshared + volatile) * page
	saved := sharing * page
	_ = reg.Collect("mem.ksm_savings", now, map[string]float64{"savings": saved, "offered": offered})
	ratio := 0.0
	if offered > 0 {
		ratio = saved * 100 / offered
	}
	_ = reg.Collect("mem.ksm_ratios", now, map[string]float64{"savings": ratio})
}

func (p *procCollector) collectWireless(reg *registry.Registry, now time.Time, ifaces []wirelessIface) {
	for _, w := range ifaces {
		id := sanitizeID(w.Name)
		if !p.m8.wifiSeen[id] {
			p.m8.wifiSeen[id] = true
			lbl := map[string]string{"device": w.Name}
			mk := func(suffix, title, units string, prio int, dims ...*registry.Dimension) {
				reg.AddChart(&registry.Chart{ID: "wireless." + suffix + "." + id, Context: "wireless." + suffix,
					Family: "wireless", Title: title + " " + w.Name, Units: units, Priority: prio,
					Plugin: "proc", Module: "wireless", Labels: lbl, Dimensions: dims})
			}
			mk("status", "Wireless link quality", "value", 3600, &registry.Dimension{ID: "link"},
				&registry.Dimension{ID: "level"}, &registry.Dimension{ID: "noise"})
			mk("discarded_packets", "Wireless discarded packets", "packets/s", 3610,
				incDim("nwid"), incDim("crypt"), incDim("frag"), incDim("retry"), incDim("misc"))
		}
		_ = reg.Collect("wireless.status."+id, now, map[string]float64{"link": w.Link, "level": w.Level, "noise": w.Noise})
		_ = reg.Collect("wireless.discarded_packets."+id, now, map[string]float64{
			"nwid": w.Nwid, "crypt": w.Crypt, "frag": w.Frag, "retry": w.Retry, "misc": w.Misc})
	}
}

func (p *procCollector) collectZram(reg *registry.Registry, now time.Time) {
	ents, err := os.ReadDir(p.m8.zram)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), "zram") {
			continue
		}
		raw, err := readTrim(filepath.Join(p.m8.zram, e.Name(), "mm_stat"))
		if err != nil {
			continue
		}
		st, ok := parseZramMMStat(raw)
		if !ok {
			continue
		}
		id := sanitizeID(e.Name())
		if !p.m8.zramSeen[id] {
			p.m8.zramSeen[id] = true
			lbl := map[string]string{"device": e.Name()}
			mk := func(suffix, title, units string, prio int, typ registry.ChartType, dims ...*registry.Dimension) {
				reg.AddChart(&registry.Chart{ID: "mem.zram_" + suffix + "." + id, Context: "mem.zram_" + suffix,
					Family: "zram", Title: title + " " + e.Name(), Units: units, Priority: prio, Type: typ,
					Plugin: "proc", Module: "zram", Labels: lbl, Dimensions: dims})
			}
			mk("usage", "ZRAM memory usage", "MiB", 1030, registry.Stacked,
				&registry.Dimension{ID: "compressed", Divisor: 1 << 20},
				&registry.Dimension{ID: "metadata", Divisor: 1 << 20})
			mk("savings", "ZRAM savings", "MiB", 1031, registry.Area,
				&registry.Dimension{ID: "original", Divisor: 1 << 20},
				&registry.Dimension{ID: "savings", Divisor: 1 << 20})
			mk("ratio", "ZRAM compression ratio", "ratio", 1032, registry.Line, &registry.Dimension{ID: "ratio"})
			mk("efficiency", "ZRAM efficiency", "percentage", 1033, registry.Line, &registry.Dimension{ID: "percent"})
		}
		meta := st.memUsed - st.compr
		if meta < 0 {
			meta = 0
		}
		savings := st.orig - st.compr
		if savings < 0 {
			savings = 0
		}
		ratio, eff := 0.0, 0.0
		if st.compr > 0 {
			ratio = st.orig / st.compr
		}
		if st.orig > 0 {
			eff = savings * 100 / st.orig
		}
		_ = reg.Collect("mem.zram_usage."+id, now, map[string]float64{"compressed": st.compr, "metadata": meta})
		_ = reg.Collect("mem.zram_savings."+id, now, map[string]float64{"original": st.orig, "savings": savings})
		_ = reg.Collect("mem.zram_ratio."+id, now, map[string]float64{"ratio": ratio})
		_ = reg.Collect("mem.zram_efficiency."+id, now, map[string]float64{"percent": eff})
	}
}

func (p *procCollector) collectBtrfs(reg *registry.Registry, now time.Time) {
	ents, err := os.ReadDir(p.m8.btrfs)
	if err != nil {
		return
	}
	for _, e := range ents {
		name := e.Name()
		if name == "features" {
			continue
		}
		root := filepath.Join(p.m8.btrfs, name)
		alloc := filepath.Join(root, "allocation")
		if _, err := os.Stat(alloc); err != nil {
			continue
		}
		label := name
		if v, err := readTrim(filepath.Join(root, "label")); err == nil && v != "" {
			label = v
		}
		id := sanitizeID(label)
		if !p.m8.btrfsSeen[id] {
			p.m8.btrfsSeen[id] = true
			lbl := map[string]string{"filesystem": label}
			for _, kind := range []string{"data", "metadata", "system"} {
				reg.AddChart(&registry.Chart{ID: "btrfs." + kind + "." + id, Context: "btrfs." + kind,
					Family: "btrfs", Title: "Btrfs " + kind + " " + label, Units: "MiB", Priority: 2600,
					Type: registry.Stacked, Plugin: "proc", Module: "btrfs", Labels: lbl,
					Dimensions: []*registry.Dimension{
						{ID: "used", Divisor: 1 << 20}, {ID: "free", Divisor: 1 << 20}}})
			}
		}
		for _, kind := range []string{"data", "metadata", "system"} {
			dir := filepath.Join(alloc, kind)
			total, _ := readFloat(filepath.Join(dir, "total_bytes"))
			used, _ := readFloat(filepath.Join(dir, "bytes_used"))
			free := total - used
			if free < 0 {
				free = 0
			}
			_ = reg.Collect("btrfs."+kind+"."+id, now, map[string]float64{"used": used, "free": free})
		}
	}
}

func parseKVFloat(s string) map[string]float64 {
	out := map[string]float64{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		v, err := strconv.ParseFloat(f[len(f)-1], 64)
		if err != nil {
			continue
		}
		out[f[0]] = v
	}
	return out
}

func parseSockstat6(s string) map[string]float64 {
	out := map[string]float64{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 {
			continue
		}
		proto := strings.ToLower(strings.TrimSuffix(f[0], ":"))
		for i := 1; i+1 < len(f); i += 2 {
			v, _ := strconv.ParseFloat(f[i+1], 64)
			out[proto+"_"+f[i]] = v
		}
	}
	return out
}

type ipvsStat struct{ conns, inPkts, outPkts, inBytes, outBytes float64 }

func parseIPVS(s string) (ipvsStat, bool) {
	var last []string
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 5 {
			last = f
		}
	}
	if len(last) < 5 {
		return ipvsStat{}, false
	}
	num := func(i int) float64 {
		n, err := strconv.ParseUint(last[i], 16, 64)
		if err != nil {
			n, _ = strconv.ParseUint(last[i], 10, 64)
		}
		return float64(n)
	}
	return ipvsStat{conns: num(0), inPkts: num(1), outPkts: num(2), inBytes: num(3), outBytes: num(4)}, true
}

// nfsProc3 is the Linux NFSv3 procedure order after the count field.
var nfsProc3 = []string{
	"null", "getattr", "setattr", "lookup", "access", "readlink", "read", "write",
	"create", "mkdir", "symlink", "mknod", "remove", "rmdir", "rename", "link",
	"readdir", "readdirplus", "fsstat", "fsinfo", "pathconf", "commit",
}

func parseRPCStats(s string) map[string]float64 {
	out := map[string]float64{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "rpc":
			if len(f) > 1 {
				out["rpc_calls"], _ = strconv.ParseFloat(f[1], 64)
			}
			if len(f) > 2 {
				out["rpc_retrans"], _ = strconv.ParseFloat(f[2], 64)
			}
			if len(f) > 3 {
				out["rpc_authrefresh"], _ = strconv.ParseFloat(f[3], 64)
			}
		case "io":
			if len(f) > 1 {
				out["io_read"], _ = strconv.ParseFloat(f[1], 64)
			}
			if len(f) > 2 {
				out["io_write"], _ = strconv.ParseFloat(f[2], 64)
			}
		case "rc":
			if len(f) > 1 {
				out["rc_hits"], _ = strconv.ParseFloat(f[1], 64)
			}
			if len(f) > 2 {
				out["rc_misses"], _ = strconv.ParseFloat(f[2], 64)
			}
			if len(f) > 3 {
				out["rc_nocache"], _ = strconv.ParseFloat(f[3], 64)
			}
		case "proc3":
			// f[1] is the declared count; remaining are counters.
			for i, name := range nfsProc3 {
				if i+2 < len(f) {
					out["proc3_"+name], _ = strconv.ParseFloat(f[i+2], 64)
				}
			}
		}
	}
	return out
}

func parseKstat(s string) map[string]float64 {
	out := map[string]float64{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 || f[0] == "name" {
			continue
		}
		v, err := strconv.ParseFloat(f[len(f)-1], 64)
		if err != nil {
			continue
		}
		out[f[0]] = v
	}
	return out
}

type wirelessIface struct {
	Name                                   string
	Link, Level, Noise                     float64
	Nwid, Crypt, Frag, Retry, Misc, Beacon float64
}

func parseWireless(s string) []wirelessIface {
	var out []wirelessIface
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "Inter") || strings.HasPrefix(line, "face") {
			continue
		}
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 4 {
			continue
		}
		num := func(i int) float64 {
			t := strings.TrimSuffix(f[i], ".")
			v, _ := strconv.ParseFloat(t, 64)
			return v
		}
		w := wirelessIface{Name: strings.TrimSpace(name), Link: num(1), Level: num(2), Noise: num(3)}
		if len(f) > 4 {
			w.Nwid = num(4)
		}
		if len(f) > 5 {
			w.Crypt = num(5)
		}
		if len(f) > 6 {
			w.Frag = num(6)
		}
		if len(f) > 7 {
			w.Retry = num(7)
		}
		if len(f) > 8 {
			w.Misc = num(8)
		}
		if len(f) > 9 {
			w.Beacon = num(9)
		}
		out = append(out, w)
	}
	return out
}

type zramMM struct{ orig, compr, memUsed float64 }

func parseZramMMStat(s string) (zramMM, bool) {
	f := strings.Fields(s)
	if len(f) < 3 {
		return zramMM{}, false
	}
	n := func(i int) float64 { v, _ := strconv.ParseFloat(f[i], 64); return v }
	return zramMM{orig: n(0), compr: n(1), memUsed: n(2)}, true
}
