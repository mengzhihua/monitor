package collect

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// procM17 is the remaining Linux proc.plugin surface: InfiniBand, QoS/tc,
// SCTP, UDP-Lite, synproxy, NUMA, pagetypeinfo, per-IRQ and per-softirq.
// Paths and the tc dump are overridable in tests.
type procM17 struct {
	haveIB, haveTC, haveSCTP, haveUDPLite, haveSynproxy bool
	haveNUMA, havePage, haveIRQ, haveSoftirq            bool
	ibRoot, sctp, synproxy, numaRoot, pagetype          string
	interrupts, softirqs, snmp, tcDump                  string
	ibSeen, tcSeen, numaSeen                            map[string]bool
	irqSeen, softSeen                                   map[string]bool
}

func (m procM17) any() bool {
	return m.haveIB || m.haveTC || m.haveSCTP || m.haveUDPLite || m.haveSynproxy ||
		m.haveNUMA || m.havePage || m.haveIRQ || m.haveSoftirq
}

func (p *procCollector) initM17(reg *registry.Registry) {
	m := &p.m17
	m.ibRoot = firstNonEmpty(m.ibRoot, "/sys/class/infiniband")
	m.sctp = firstNonEmpty(m.sctp, "/proc/net/sctp/snmp")
	m.synproxy = firstNonEmpty(m.synproxy, "/proc/net/stat/synproxy")
	m.numaRoot = firstNonEmpty(m.numaRoot, "/sys/devices/system/node")
	m.pagetype = firstNonEmpty(m.pagetype, "/proc/pagetypeinfo")
	m.interrupts = firstNonEmpty(m.interrupts, "/proc/interrupts")
	m.softirqs = firstNonEmpty(m.softirqs, "/proc/softirqs")
	m.snmp = firstNonEmpty(m.snmp, "/proc/net/snmp")
	m.ibSeen, m.tcSeen, m.numaSeen = map[string]bool{}, map[string]bool{}, map[string]bool{}
	m.irqSeen, m.softSeen = map[string]bool{}, map[string]bool{}

	if ents, err := os.ReadDir(m.ibRoot); err == nil {
		for _, e := range ents {
			if _, err := os.Stat(filepath.Join(m.ibRoot, e.Name(), "ports")); err == nil {
				m.haveIB = true
				break
			}
		}
	}
	if raw, err := readTrim(m.sctp); err == nil && strings.Contains(raw, "SctpCurrEstab") {
		m.haveSCTP = true
		addSCTPCharts(reg)
	}
	if raw, err := readTrim(m.snmp); err == nil {
		if stats := parseSNMP(raw); stats["UdpLite"] != nil || stats["Udplite"] != nil {
			m.haveUDPLite = true
			addUDPLiteCharts(reg)
		}
	}
	if _, err := readTrim(m.synproxy); err == nil {
		m.haveSynproxy = true
		nf := func(id, title, units string, prio int, dims ...*registry.Dimension) {
			ch := sysChart(id, "netfilter", title, units, prio, dims...)
			ch.Plugin, ch.Module = "proc", "synproxy"
			reg.AddChart(ch)
		}
		nf("netfilter.synproxy_syn_received", "SYNPROXY SYN received", "events/s", 8060, incDim("received"))
		nf("netfilter.synproxy_conn_reopened", "SYNPROXY connections reopened", "connections/s", 8061, incDim("reopened"))
		nf("netfilter.synproxy_cookies", "SYNPROXY cookies", "cookies/s", 8062,
			incDim("valid"), incDim("invalid"), incDim("retrans"))
	}
	if ents, err := os.ReadDir(m.numaRoot); err == nil {
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), "node") {
				if _, err := readTrim(filepath.Join(m.numaRoot, e.Name(), "numastat")); err == nil {
					m.haveNUMA = true
					break
				}
			}
		}
	}
	if raw, err := readTrim(m.pagetype); err == nil && (strings.Contains(raw, "Free pages") || strings.Contains(raw, ", type")) {
		m.havePage = true
	}
	if raw, err := readTrim(m.interrupts); err == nil && strings.Contains(raw, "CPU") {
		m.haveIRQ = true
		ch := sysChart("system.interrupts", "interrupts", "CPU interrupts by source", "interrupts/s", 1000)
		ch.Type = registry.Stacked
		ch.Plugin, ch.Module = "proc", "interrupts"
		reg.AddChart(ch)
		cpu := sysChart("cpu.interrupts", "interrupts", "CPU interrupts per core", "interrupts/s", 1001)
		cpu.Type = registry.Stacked
		cpu.Plugin, cpu.Module = "proc", "interrupts"
		reg.AddChart(cpu)
		_ = raw
	}
	if raw, err := readTrim(m.softirqs); err == nil && strings.Contains(raw, "CPU") {
		m.haveSoftirq = true
		ch := sysChart("system.softirqs", "softirqs", "Softirqs by type", "softirqs/s", 1002)
		ch.Type = registry.Stacked
		ch.Plugin, ch.Module = "proc", "softirqs"
		reg.AddChart(ch)
		_ = raw
	}
	if m.tcDump != "" {
		m.haveTC = true
	} else if _, err := exec.LookPath("tc"); err == nil {
		m.haveTC = true
	}
}

func addSCTPCharts(reg *registry.Registry) {
	sctp := func(id, title, units string, prio int, dims ...*registry.Dimension) {
		ch := sysChart(id, "sctp", title, units, prio, dims...)
		ch.Plugin, ch.Module = "proc", "sctp"
		reg.AddChart(ch)
	}
	sctp("sctp.established", "SCTP established associations", "associations", 7000, &registry.Dimension{ID: "established"})
	sctp("sctp.transitions", "SCTP association transitions", "transitions/s", 7010,
		incDim("active"), incDim("passive"), incDim("aborted"), incDim("shutdown"))
	sctp("sctp.packets", "SCTP packets", "packets/s", 7020, incDim("received"),
		&registry.Dimension{ID: "sent", Algorithm: registry.Incremental, Multiplier: -1})
	sctp("sctp.packet_errors", "SCTP packet errors", "packets/s", 7021,
		incDim("invalid"), incDim("discarded"), incDim("checksum"))
	sctp("sctp.fragmentation", "SCTP fragmentation", "events/s", 7030,
		incDim("reassembled"), incDim("fragmented"))
	sctp("sctp.chunk", "SCTP control chunks", "chunks/s", 7040,
		incDim("out_ctrl"), incDim("in_ctrl"), incDim("out_order"), incDim("in_order"))
}

func addUDPLiteCharts(reg *registry.Registry) {
	ipFamily := func(id, title, units string, prio int, dims ...*registry.Dimension) {
		ch := sysChart(id, "ip", title, units, prio, dims...)
		ch.Plugin, ch.Module = "proc", "udplite"
		reg.AddChart(ch)
	}
	ipFamily("ipv4.udplitepackets", "IPv4 UDP-Lite packets", "packets/s", 3440, incDim("received"),
		&registry.Dimension{ID: "sent", Algorithm: registry.Incremental, Multiplier: -1})
	ipFamily("ipv4.udpliteerrors", "IPv4 UDP-Lite errors", "events/s", 3441,
		incDim("InErrors"), incDim("NoPorts"), incDim("RcvbufErrors"), incDim("SndbufErrors"), incDim("InCsumErrors"))
}

func (p *procCollector) collectM17(reg *registry.Registry, now time.Time) {
	m := &p.m17
	if m.haveIB {
		p.collectIB(reg, now)
	}
	if m.haveTC {
		p.collectTC(reg, now)
	}
	if m.haveSCTP {
		if raw, err := readTrim(m.sctp); err == nil {
			st := parseSCTPSnmp(raw)
			_ = reg.Collect("sctp.established", now, map[string]float64{"established": st["SctpCurrEstab"]})
			_ = reg.Collect("sctp.transitions", now, map[string]float64{
				"active": st["SctpActiveEstabs"], "passive": st["SctpPassiveEstabs"],
				"aborted": st["SctpAborteds"], "shutdown": st["SctpShutdowns"]})
			_ = reg.Collect("sctp.packets", now, map[string]float64{
				"received": st["SctpInSCTPPacks"], "sent": st["SctpOutSCTPPacks"]})
			_ = reg.Collect("sctp.packet_errors", now, map[string]float64{
				"invalid": st["SctpInInvalid"], "discarded": st["SctpInPktDiscards"], "checksum": st["SctpChecksumErrors"]})
			_ = reg.Collect("sctp.fragmentation", now, map[string]float64{
				"reassembled": st["SctpReasmUsrMsgs"], "fragmented": st["SctpFragUsrMsgs"]})
			_ = reg.Collect("sctp.chunk", now, map[string]float64{
				"out_ctrl": st["SctpOutCtrlChunks"], "in_ctrl": st["SctpInCtrlChunks"],
				"out_order": st["SctpOutOrderChunks"], "in_order": st["SctpInOrderChunks"]})
		}
	}
	if m.haveUDPLite {
		if raw, err := readTrim(m.snmp); err == nil {
			stats := parseSNMP(raw)
			proto := "UdpLite"
			if stats[proto] == nil {
				proto = "Udplite"
			}
			_ = reg.Collect("ipv4.udplitepackets", now, map[string]float64{
				"received": snmpGet(stats, proto, "InDatagrams"), "sent": snmpGet(stats, proto, "OutDatagrams")})
			_ = reg.Collect("ipv4.udpliteerrors", now, map[string]float64{
				"InErrors": snmpGet(stats, proto, "InErrors"), "NoPorts": snmpGet(stats, proto, "NoPorts"),
				"RcvbufErrors": snmpGet(stats, proto, "RcvbufErrors"), "SndbufErrors": snmpGet(stats, proto, "SndbufErrors"),
				"InCsumErrors": snmpGet(stats, proto, "InCsumErrors")})
		}
	}
	if m.haveSynproxy {
		if raw, err := readTrim(m.synproxy); err == nil {
			st := parseSynproxy(raw)
			_ = reg.Collect("netfilter.synproxy_syn_received", now, map[string]float64{"received": st["syn_received"]})
			_ = reg.Collect("netfilter.synproxy_conn_reopened", now, map[string]float64{"reopened": st["conn_reopened"]})
			_ = reg.Collect("netfilter.synproxy_cookies", now, map[string]float64{
				"valid": st["cookie_valid"], "invalid": st["cookie_invalid"], "retrans": st["cookie_retrans"]})
		}
	}
	if m.haveNUMA {
		p.collectNUMA(reg, now)
	}
	if m.havePage {
		if raw, err := readTrim(m.pagetype); err == nil {
			p.collectPageType(reg, now, parsePageType(raw))
		}
	}
	if m.haveIRQ {
		if raw, err := readTrim(m.interrupts); err == nil {
			p.collectInterrupts(reg, now, parseInterrupts(raw))
		}
	}
	if m.haveSoftirq {
		if raw, err := readTrim(m.softirqs); err == nil {
			p.collectSoftirqs(reg, now, parseSoftirqs(raw))
		}
	}
}

func (p *procCollector) collectIB(reg *registry.Registry, now time.Time) {
	devs, err := os.ReadDir(p.m17.ibRoot)
	if err != nil {
		return
	}
	for _, d := range devs {
		portsRoot := filepath.Join(p.m17.ibRoot, d.Name(), "ports")
		ports, err := os.ReadDir(portsRoot)
		if err != nil {
			continue
		}
		for _, port := range ports {
			ctr := filepath.Join(portsRoot, port.Name(), "counters")
			xmit, _ := readFloat(filepath.Join(ctr, "port_xmit_data"))
			rcv, _ := readFloat(filepath.Join(ctr, "port_rcv_data"))
			xmitPkts, _ := readFloat(filepath.Join(ctr, "port_xmit_packets"))
			rcvPkts, _ := readFloat(filepath.Join(ctr, "port_rcv_packets"))
			disc, _ := readFloat(filepath.Join(ctr, "port_xmit_discards"))
			sym, _ := readFloat(filepath.Join(ctr, "symbol_error"))
			link, _ := readFloat(filepath.Join(ctr, "link_error_recovery"))
			id := sanitizeID(d.Name() + "_" + port.Name())
			p.ensureIBCharts(reg, d.Name(), port.Name(), id)
			_ = reg.Collect("ib.port_bytes."+id, now, map[string]float64{"received": rcv * 4, "sent": xmit * 4})
			_ = reg.Collect("ib.port_packets."+id, now, map[string]float64{"received": rcvPkts, "sent": xmitPkts})
			_ = reg.Collect("ib.port_errors."+id, now, map[string]float64{
				"xmit_discards": disc, "symbol": sym, "link_recovery": link})
		}
	}
}

func (p *procCollector) ensureIBCharts(reg *registry.Registry, dev, port, id string) {
	if p.m17.ibSeen[id] {
		return
	}
	p.m17.ibSeen[id] = true
	lbl := map[string]string{"device": dev, "port": port}
	bytes := sysChart("ib.port_bytes."+id, "infiniband", "InfiniBand port "+dev+"/"+port+" bandwidth", "bytes/s", 5400,
		incDim("received"), &registry.Dimension{ID: "sent", Algorithm: registry.Incremental, Multiplier: -1})
	bytes.Context, bytes.Labels, bytes.Type = "ib.port_bytes", lbl, registry.Area
	bytes.Plugin, bytes.Module = "proc", "infiniband"
	reg.AddChart(bytes)
	pkts := sysChart("ib.port_packets."+id, "infiniband", "InfiniBand port "+dev+"/"+port+" packets", "packets/s", 5410,
		incDim("received"), &registry.Dimension{ID: "sent", Algorithm: registry.Incremental, Multiplier: -1})
	pkts.Context, pkts.Labels = "ib.port_packets", lbl
	pkts.Plugin, pkts.Module = "proc", "infiniband"
	reg.AddChart(pkts)
	errc := sysChart("ib.port_errors."+id, "infiniband", "InfiniBand port "+dev+"/"+port+" errors", "errors/s", 5420,
		incDim("xmit_discards"), incDim("symbol"), incDim("link_recovery"))
	errc.Context, errc.Labels = "ib.port_errors", lbl
	errc.Plugin, errc.Module = "proc", "infiniband"
	reg.AddChart(errc)
}

func (p *procCollector) collectTC(reg *registry.Registry, now time.Time) {
	raw := p.m17.tcDump
	if raw == "" {
		out, err := execRun(3*time.Second)(context.Background(), "tc", "-s", "qdisc")
		if err != nil {
			return
		}
		raw = string(out)
	}
	for _, q := range parseTC(raw) {
		id := sanitizeID(q.Dev + "_" + q.Handle)
		p.ensureTCCharts(reg, q, id)
		_ = reg.Collect("tc.qos_bytes."+id, now, map[string]float64{"sent": q.Bytes})
		_ = reg.Collect("tc.qos_packets."+id, now, map[string]float64{"sent": q.Packets})
		_ = reg.Collect("tc.qos_dropped."+id, now, map[string]float64{"dropped": q.Dropped, "overlimits": q.Overlimits})
	}
}

func (p *procCollector) ensureTCCharts(reg *registry.Registry, q tcQdisc, id string) {
	if p.m17.tcSeen[id] {
		return
	}
	p.m17.tcSeen[id] = true
	lbl := map[string]string{"device": q.Dev, "handle": q.Handle, "kind": q.Kind}
	b := sysChart("tc.qos_bytes."+id, "qos", "QoS "+q.Dev+" "+q.Handle+" bandwidth", "bytes/s", 7000, incDim("sent"))
	b.Context, b.Labels, b.Plugin, b.Module = "tc.qos_bytes", lbl, "tc", "qos"
	reg.AddChart(b)
	pk := sysChart("tc.qos_packets."+id, "qos", "QoS "+q.Dev+" "+q.Handle+" packets", "packets/s", 7010, incDim("sent"))
	pk.Context, pk.Labels, pk.Plugin, pk.Module = "tc.qos_packets", lbl, "tc", "qos"
	reg.AddChart(pk)
	dr := sysChart("tc.qos_dropped."+id, "qos", "QoS "+q.Dev+" "+q.Handle+" drops", "packets/s", 7020, incDim("dropped"), incDim("overlimits"))
	dr.Context, dr.Labels, dr.Plugin, dr.Module = "tc.qos_dropped", lbl, "tc", "qos"
	reg.AddChart(dr)
}

func (p *procCollector) collectNUMA(reg *registry.Registry, now time.Time) {
	ents, err := os.ReadDir(p.m17.numaRoot)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), "node") {
			continue
		}
		raw, err := readTrim(filepath.Join(p.m17.numaRoot, e.Name(), "numastat"))
		if err != nil {
			continue
		}
		st := parseNumaStat(raw)
		id := sanitizeID(e.Name())
		p.ensureNUMACharts(reg, e.Name(), id)
		_ = reg.Collect("mem.numa_events."+id, now, map[string]float64{
			"hit": st["numa_hit"], "miss": st["numa_miss"], "foreign": st["numa_foreign"],
			"interleave": st["interleave_hit"], "local": st["local_node"], "other": st["other_node"]})
	}
}

func (p *procCollector) ensureNUMACharts(reg *registry.Registry, node, id string) {
	if p.m17.numaSeen[id] {
		return
	}
	p.m17.numaSeen[id] = true
	ch := sysChart("mem.numa_events."+id, "numa", "NUMA node "+node+" events", "events/s", 1025,
		incDim("hit"), incDim("miss"), incDim("foreign"), incDim("interleave"), incDim("local"), incDim("other"))
	ch.Context, ch.Labels = "mem.numa_events", map[string]string{"numa_node": node}
	ch.Plugin, ch.Module = "proc", "numa"
	reg.AddChart(ch)
}

func (p *procCollector) collectPageType(reg *registry.Registry, now time.Time, zones map[string]map[string]float64) {
	for zone, types := range zones {
		id := sanitizeID(zone)
		cid := "mem.pagetype_" + id
		if _, ok := reg.Chart(cid); !ok {
			dims := []*registry.Dimension{}
			for _, n := range []string{"Unmovable", "Reclaimable", "Movable", "Reserve", "CMA", "Isolate"} {
				dims = append(dims, &registry.Dimension{ID: n})
			}
			ch := sysChart(cid, "pagetype", "Free pages in zone "+zone+" by migrate type", "pages", 1030, dims...)
			ch.Context, ch.Type = "mem.pagetype", registry.Stacked
			ch.Labels = map[string]string{"zone": zone}
			ch.Plugin, ch.Module = "proc", "pagetypeinfo"
			reg.AddChart(ch)
		}
		_ = reg.Collect(cid, now, types)
	}
}

func (p *procCollector) collectInterrupts(reg *registry.Registry, now time.Time, ir interrupts) {
	if ch, ok := reg.Chart("system.interrupts"); ok {
		vals := map[string]float64{}
		for name, n := range ir.byName {
			id := sanitizeID(name)
			if !p.m17.irqSeen[id] {
				ch.AddDimension(incDim(id))
				p.m17.irqSeen[id] = true
			}
			vals[id] = n
		}
		_ = reg.Collect("system.interrupts", now, vals)
	}
	if ch, ok := reg.Chart("cpu.interrupts"); ok {
		vals := map[string]float64{}
		for i, n := range ir.byCPU {
			id := "cpu" + strconv.Itoa(i)
			if !p.m17.irqSeen["cpu:"+id] {
				ch.AddDimension(incDim(id))
				p.m17.irqSeen["cpu:"+id] = true
			}
			vals[id] = n
		}
		_ = reg.Collect("cpu.interrupts", now, vals)
	}
}

func (p *procCollector) collectSoftirqs(reg *registry.Registry, now time.Time, byType map[string]float64) {
	ch, ok := reg.Chart("system.softirqs")
	if !ok {
		return
	}
	vals := map[string]float64{}
	for name, n := range byType {
		id := sanitizeID(name)
		if !p.m17.softSeen[id] {
			ch.AddDimension(incDim(id))
			p.m17.softSeen[id] = true
		}
		vals[id] = n
	}
	_ = reg.Collect("system.softirqs", now, vals)
}

func parseSCTPSnmp(s string) map[string]float64 {
	out := map[string]float64{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		out[strings.TrimSuffix(f[0], ":")], _ = strconv.ParseFloat(f[1], 64)
	}
	return out
}

func parseSynproxy(s string) map[string]float64 {
	out := map[string]float64{}
	var keys []string
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		if keys == nil {
			keys = f
			continue
		}
		for i, k := range keys {
			if i >= len(f) {
				break
			}
			v, err := strconv.ParseUint(f[i], 16, 64)
			if err != nil {
				v2, _ := strconv.ParseFloat(f[i], 64)
				out[k] += v2
				continue
			}
			out[k] += float64(v)
		}
	}
	return out
}

func parseNumaStat(s string) map[string]float64 {
	out := map[string]float64{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		out[f[0]], _ = strconv.ParseFloat(f[1], 64)
	}
	return out
}

func parsePageType(s string) map[string]map[string]float64 {
	out := map[string]map[string]float64{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, ", type") {
			continue
		}
		// Node 0, zone      DMA, type    Unmovable      1 2 3 ...
		zone, typ := "", ""
		if i := strings.Index(line, "zone"); i >= 0 {
			rest := strings.TrimSpace(line[i+4:])
			if j := strings.Index(rest, ","); j > 0 {
				zone = strings.TrimSpace(rest[:j])
				rest = rest[j+1:]
			}
			if k := strings.Index(rest, "type"); k >= 0 {
				rest = strings.TrimSpace(rest[k+4:])
				fields := strings.Fields(rest)
				if len(fields) >= 2 {
					typ = fields[0]
					var sum float64
					for _, n := range fields[1:] {
						v, _ := strconv.ParseFloat(n, 64)
						sum += v
					}
					if out[zone] == nil {
						out[zone] = map[string]float64{}
					}
					out[zone][typ] += sum
				}
			}
		}
	}
	return out
}

type interrupts struct {
	byName map[string]float64
	byCPU  []float64
}

func parseInterrupts(s string) interrupts {
	out := interrupts{byName: map[string]float64{}}
	sc := bufio.NewScanner(strings.NewReader(s))
	cpus := 0
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		if strings.HasPrefix(f[0], "CPU") {
			cpus = len(f)
			out.byCPU = make([]float64, cpus)
			continue
		}
		if !strings.HasSuffix(f[0], ":") || len(f) < 2 {
			continue
		}
		end := 1 + cpus
		if cpus == 0 {
			end = 2
		}
		if end > len(f) {
			end = len(f)
		}
		name := strings.TrimSuffix(f[0], ":")
		if _, err := strconv.Atoi(name); err == nil && end < len(f) {
			for i := len(f) - 1; i >= end; i-- {
				if _, err := strconv.ParseFloat(f[i], 64); err != nil {
					name = f[i]
					break
				}
			}
		}
		var total float64
		for i := 1; i < end; i++ {
			v, _ := strconv.ParseFloat(f[i], 64)
			total += v
			if i-1 < len(out.byCPU) {
				out.byCPU[i-1] += v
			}
		}
		out.byName[name] += total
	}
	return out
}

func parseSoftirqs(s string) map[string]float64 {
	out := map[string]float64{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 || strings.HasPrefix(f[0], "CPU") {
			continue
		}
		name := strings.TrimSuffix(f[0], ":")
		var sum float64
		for _, n := range f[1:] {
			v, _ := strconv.ParseFloat(n, 64)
			sum += v
		}
		out[name] = sum
	}
	return out
}

type tcQdisc struct {
	Dev, Handle, Kind                   string
	Bytes, Packets, Dropped, Overlimits float64
}

func parseTC(s string) []tcQdisc {
	var out []tcQdisc
	var cur *tcQdisc
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "qdisc ") {
			f := strings.Fields(line)
			q := tcQdisc{Kind: f[1]}
			for i, tok := range f {
				if tok == "dev" && i+1 < len(f) {
					q.Dev = f[i+1]
				}
				if i == 2 {
					q.Handle = strings.TrimSuffix(tok, ":")
				}
			}
			out = append(out, q)
			cur = &out[len(out)-1]
			continue
		}
		if cur == nil {
			continue
		}
		if strings.HasPrefix(line, "Sent ") {
			f := strings.Fields(line)
			// Sent 123 bytes 4 pkt (dropped 1, overlimits 0 requeues 0)
			if len(f) >= 5 {
				cur.Bytes, _ = strconv.ParseFloat(f[1], 64)
				cur.Packets, _ = strconv.ParseFloat(f[3], 64)
			}
			if i := strings.Index(line, "dropped"); i >= 0 {
				rest := line[i:]
				cur.Dropped = firstFloat(rest)
			}
			if i := strings.Index(line, "overlimits"); i >= 0 {
				cur.Overlimits = firstFloat(line[i:])
			}
		}
	}
	return out
}
