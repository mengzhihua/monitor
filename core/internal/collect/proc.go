package collect

import (
	"bufio"
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// procCollector reads Linux /proc extras that gopsutil does not cover:
// entropy, file descriptors, forks/interrupts from /proc/stat, PSI,
// IPv4 protocol counters from /proc/net/snmp, conntrack, softnet, SysV IPC,
// mdstat, power_supply, IPv6 SNMP/sockstat, IPVS, NFS, ZFS ARC, Btrfs,
// wireless, KSM and zram, InfiniBand, QoS/tc, SCTP, UDP-Lite, synproxy,
// NUMA, pagetypeinfo and per-IRQ / softirq detail. Init fails on other OSes.
type procCollector struct {
	haveEntropy, haveFD, haveStat, haveSNMP bool
	pressure                                []string // resource names that exist
	haveConntrack, haveSoftnet, haveIPC     bool
	haveMD, havePower                       bool
	mdSeen, psSeen                          map[string]bool
	// roots are overridable in tests; empty = production paths.
	mdPath, psRoot, connCount, connMax, connStat, softnet string
	ipcShm, ipcMsg, ipcSem                                string
	m8                                                    procM8
	m17                                                   procM17
}

func init() {
	Register("proc", func() Collector { return &procCollector{} })
}

func (p *procCollector) Name() string { return "proc" }

func (p *procCollector) Init(reg *registry.Registry) error {
	if _, err := readTrim("/proc/stat"); err == nil {
		p.haveStat = true
		reg.AddChart(sysChart("system.intr", "processes", "CPU interrupts", "interrupts/s", 212, incDim("interrupts")))
		reg.AddChart(sysChart("system.forks", "processes", "Started processes", "processes/s", 213, incDim("started")))
	}
	if _, err := readTrim("/proc/sys/kernel/random/entropy_avail"); err == nil {
		p.haveEntropy = true
		reg.AddChart(sysChart("system.entropy", "entropy", "Available entropy", "entropy", 214,
			&registry.Dimension{ID: "entropy"}))
	}
	if _, err := readTrim("/proc/sys/fs/file-nr"); err == nil {
		p.haveFD = true
		reg.AddChart(sysChart("system.file_nr", "file descriptors", "File descriptors", "descriptors", 215,
			&registry.Dimension{ID: "allocated"}, &registry.Dimension{ID: "unused"}, &registry.Dimension{ID: "max", Hidden: true}))
	}
	for _, res := range []string{"cpu", "memory", "io", "irq"} {
		if _, err := readTrim("/proc/pressure/" + res); err == nil {
			p.pressure = append(p.pressure, res)
			id := "system." + res
			title := strings.ToUpper(res[:1]) + res[1:]
			reg.AddChart(sysChart(id+"_some_pressure", "pressure", title+" some pressure stall", "percentage", 220,
				&registry.Dimension{ID: "avg10"}, &registry.Dimension{ID: "avg60"}, &registry.Dimension{ID: "avg300"}))
			reg.AddChart(sysChart(id+"_some_pressure_stall_time", "pressure", title+" some stall time", "ms/s", 221,
				incDim("stall_time")))
			reg.AddChart(sysChart(id+"_full_pressure", "pressure", title+" full pressure stall", "percentage", 222,
				&registry.Dimension{ID: "avg10"}, &registry.Dimension{ID: "avg60"}, &registry.Dimension{ID: "avg300"}))
			reg.AddChart(sysChart(id+"_full_pressure_stall_time", "pressure", title+" full stall time", "ms/s", 223,
				incDim("stall_time")))
		}
	}
	if raw, err := readTrim("/proc/net/snmp"); err == nil {
		if stats := parseSNMP(raw); stats["Tcp"] != nil || stats["Ip"] != nil {
			p.haveSNMP = true
			addIPCharts(reg)
		}
	}
	p.initExtras(reg)
	p.initM8(reg)
	p.initM17(reg)
	if !p.haveStat && !p.haveEntropy && !p.haveFD && !p.haveSNMP && len(p.pressure) == 0 &&
		!p.haveConntrack && !p.haveSoftnet && !p.haveIPC && !p.haveMD && !p.havePower && !p.m8.any() && !p.m17.any() {
		return errors.New("no /proc metrics available")
	}
	return nil
}

func addIPCharts(reg *registry.Registry) {
	ipFamily := func(id, title, units string, prio int, dims ...*registry.Dimension) {
		ch := sysChart(id, "ip", title, units, prio, dims...)
		ch.Plugin, ch.Module = "proc", "snmp"
		reg.AddChart(ch)
	}
	ipFamily("ipv4.packets", "IPv4 packets", "packets/s", 3400, incDim("received"),
		&registry.Dimension{ID: "sent", Algorithm: registry.Incremental, Multiplier: -1},
		incDim("forwarded"), incDim("delivered"))
	ipFamily("ipv4.errors", "IPv4 errors", "packets/s", 3401,
		incDim("InDiscards"), incDim("OutDiscards"), incDim("InHdrErrors"), incDim("InAddrErrors"), incDim("OutNoRoutes"))
	ipFamily("ipv4.tcpsock", "IPv4 TCP connections", "connections", 3410, &registry.Dimension{ID: "connections"})
	ipFamily("ipv4.tcppackets", "IPv4 TCP packets", "packets/s", 3411, incDim("received"),
		&registry.Dimension{ID: "sent", Algorithm: registry.Incremental, Multiplier: -1})
	ipFamily("ipv4.tcperrors", "IPv4 TCP errors", "packets/s", 3412,
		incDim("InErrs"), incDim("InCsumErrors"), incDim("RetransSegs"), incDim("OutRsts"))
	ipFamily("ipv4.tcphandshake", "IPv4 TCP handshake", "events/s", 3413,
		incDim("ActiveOpens"), incDim("PassiveOpens"), incDim("AttemptFails"), incDim("EstabResets"))
	ipFamily("ipv4.udppackets", "IPv4 UDP packets", "packets/s", 3420, incDim("received"),
		&registry.Dimension{ID: "sent", Algorithm: registry.Incremental, Multiplier: -1})
	ipFamily("ipv4.udperrors", "IPv4 UDP errors", "events/s", 3421,
		incDim("InErrors"), incDim("NoPorts"), incDim("RcvbufErrors"), incDim("SndbufErrors"), incDim("InCsumErrors"))
	ipFamily("ipv4.icmp", "IPv4 ICMP messages", "messages/s", 3430, incDim("received"),
		&registry.Dimension{ID: "sent", Algorithm: registry.Incremental, Multiplier: -1})
	ipFamily("ipv4.icmpmsg", "IPv4 ICMP messages by type", "messages/s", 3431,
		incDim("InEchoReps"), incDim("OutEchoReps"), incDim("InEchos"), incDim("OutEchos"))
}

func (p *procCollector) Collect(_ context.Context, reg *registry.Registry, now time.Time) error {
	if p.haveStat {
		if st, err := parseProcStatFile("/proc/stat"); err == nil {
			_ = reg.Collect("system.intr", now, map[string]float64{"interrupts": st.intr})
			_ = reg.Collect("system.forks", now, map[string]float64{"started": st.forks})
		}
	}
	if p.haveEntropy {
		if v, err := readFloat("/proc/sys/kernel/random/entropy_avail"); err == nil {
			_ = reg.Collect("system.entropy", now, map[string]float64{"entropy": v})
		}
	}
	if p.haveFD {
		if a, u, m, err := parseFileNRFile("/proc/sys/fs/file-nr"); err == nil {
			_ = reg.Collect("system.file_nr", now, map[string]float64{"allocated": a, "unused": u, "max": m})
		}
	}
	for _, res := range p.pressure {
		raw, err := readTrim("/proc/pressure/" + res)
		if err != nil {
			continue
		}
		pr := parsePressure(raw)
		id := "system." + res
		_ = reg.Collect(id+"_some_pressure", now, map[string]float64{"avg10": pr.some10, "avg60": pr.some60, "avg300": pr.some300})
		_ = reg.Collect(id+"_some_pressure_stall_time", now, map[string]float64{"stall_time": pr.someTotal / 1000}) // µs → ms
		if _, ok := reg.Chart(id + "_full_pressure"); ok {
			_ = reg.Collect(id+"_full_pressure", now, map[string]float64{"avg10": pr.full10, "avg60": pr.full60, "avg300": pr.full300})
			_ = reg.Collect(id+"_full_pressure_stall_time", now, map[string]float64{"stall_time": pr.fullTotal / 1000})
		}
	}
	if p.haveSNMP {
		raw, err := readTrim("/proc/net/snmp")
		if err == nil {
			collectSNMP(reg, now, parseSNMP(raw))
		}
	}
	p.collectExtras(reg, now)
	p.collectM8(reg, now)
	p.collectM17(reg, now)
	return nil
}

type procStatExtra struct{ intr, forks float64 }

func parseProcStat(s string) procStatExtra {
	var out procStatExtra
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "intr":
			out.intr, _ = strconv.ParseFloat(f[1], 64)
		case "processes":
			out.forks, _ = strconv.ParseFloat(f[1], 64)
		}
	}
	return out
}

func parseProcStatFile(path string) (procStatExtra, error) {
	s, err := readTrim(path)
	if err != nil {
		return procStatExtra{}, err
	}
	return parseProcStat(s), nil
}

func readFloat(path string) (float64, error) {
	s, err := readTrim(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(strings.Fields(s)[0], 64)
}

func parseFileNR(s string) (allocated, unused, max float64, err error) {
	f := strings.Fields(s)
	if len(f) < 3 {
		return 0, 0, 0, errors.New("file-nr: expected 3 fields")
	}
	allocated, _ = strconv.ParseFloat(f[0], 64)
	unused, _ = strconv.ParseFloat(f[1], 64)
	max, _ = strconv.ParseFloat(f[2], 64)
	return
}

func parseFileNRFile(path string) (float64, float64, float64, error) {
	s, err := readTrim(path)
	if err != nil {
		return 0, 0, 0, err
	}
	return parseFileNR(s)
}

type pressure struct {
	some10, some60, some300, someTotal float64
	full10, full60, full300, fullTotal float64
}

func parsePressure(s string) pressure {
	var p pressure
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		avg := func(tok string) float64 {
			_, v, _ := strings.Cut(tok, "=")
			n, _ := strconv.ParseFloat(v, 64)
			return n
		}
		switch f[0] {
		case "some":
			p.some10, p.some60, p.some300, p.someTotal = avg(f[1]), avg(f[2]), avg(f[3]), avg(f[4])
		case "full":
			p.full10, p.full60, p.full300, p.fullTotal = avg(f[1]), avg(f[2]), avg(f[3]), avg(f[4])
		}
	}
	return p
}

// parseSNMP turns /proc/net/snmp's paired header/value lines into
// protocol → field → value. Unknown / truncated pairs are skipped.
func parseSNMP(s string) map[string]map[string]float64 {
	out := map[string]map[string]float64{}
	lines := strings.Split(s, "\n")
	for i := 0; i+1 < len(lines); i += 2 {
		h, v := strings.Fields(lines[i]), strings.Fields(lines[i+1])
		if len(h) < 2 || len(v) < 2 || h[0] != v[0] {
			continue
		}
		proto := strings.TrimSuffix(h[0], ":")
		m := map[string]float64{}
		n := min(len(h), len(v))
		for j := 1; j < n; j++ {
			m[h[j]], _ = strconv.ParseFloat(v[j], 64)
		}
		out[proto] = m
	}
	return out
}

func snmpGet(m map[string]map[string]float64, proto, key string) float64 {
	if p := m[proto]; p != nil {
		return p[key]
	}
	return 0
}

func collectSNMP(reg *registry.Registry, now time.Time, m map[string]map[string]float64) {
	_ = reg.Collect("ipv4.packets", now, map[string]float64{
		"received": snmpGet(m, "Ip", "InReceives"), "sent": snmpGet(m, "Ip", "OutRequests"),
		"forwarded": snmpGet(m, "Ip", "ForwDatagrams"), "delivered": snmpGet(m, "Ip", "InDelivers")})
	_ = reg.Collect("ipv4.errors", now, map[string]float64{
		"InDiscards": snmpGet(m, "Ip", "InDiscards"), "OutDiscards": snmpGet(m, "Ip", "OutDiscards"),
		"InHdrErrors": snmpGet(m, "Ip", "InHdrErrors"), "InAddrErrors": snmpGet(m, "Ip", "InAddrErrors"),
		"OutNoRoutes": snmpGet(m, "Ip", "OutNoRoutes")})
	_ = reg.Collect("ipv4.tcpsock", now, map[string]float64{"connections": snmpGet(m, "Tcp", "CurrEstab")})
	_ = reg.Collect("ipv4.tcppackets", now, map[string]float64{
		"received": snmpGet(m, "Tcp", "InSegs"), "sent": snmpGet(m, "Tcp", "OutSegs")})
	_ = reg.Collect("ipv4.tcperrors", now, map[string]float64{
		"InErrs": snmpGet(m, "Tcp", "InErrs"), "InCsumErrors": snmpGet(m, "Tcp", "InCsumErrors"),
		"RetransSegs": snmpGet(m, "Tcp", "RetransSegs"), "OutRsts": snmpGet(m, "Tcp", "OutRsts")})
	_ = reg.Collect("ipv4.tcphandshake", now, map[string]float64{
		"ActiveOpens": snmpGet(m, "Tcp", "ActiveOpens"), "PassiveOpens": snmpGet(m, "Tcp", "PassiveOpens"),
		"AttemptFails": snmpGet(m, "Tcp", "AttemptFails"), "EstabResets": snmpGet(m, "Tcp", "EstabResets")})
	_ = reg.Collect("ipv4.udppackets", now, map[string]float64{
		"received": snmpGet(m, "Udp", "InDatagrams"), "sent": snmpGet(m, "Udp", "OutDatagrams")})
	_ = reg.Collect("ipv4.udperrors", now, map[string]float64{
		"InErrors": snmpGet(m, "Udp", "InErrors"), "NoPorts": snmpGet(m, "Udp", "NoPorts"),
		"RcvbufErrors": snmpGet(m, "Udp", "RcvbufErrors"), "SndbufErrors": snmpGet(m, "Udp", "SndbufErrors"),
		"InCsumErrors": snmpGet(m, "Udp", "InCsumErrors")})
	_ = reg.Collect("ipv4.icmp", now, map[string]float64{
		"received": snmpGet(m, "Icmp", "InMsgs"), "sent": snmpGet(m, "Icmp", "OutMsgs")})
	_ = reg.Collect("ipv4.icmpmsg", now, map[string]float64{
		"InEchoReps": snmpGet(m, "Icmp", "InEchoReps"), "OutEchoReps": snmpGet(m, "Icmp", "OutEchoReps"),
		"InEchos": snmpGet(m, "Icmp", "InEchos"), "OutEchos": snmpGet(m, "Icmp", "OutEchos")})
}
