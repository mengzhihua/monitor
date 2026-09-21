package collect

import (
	"bufio"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func (p *procCollector) initExtras(reg *registry.Registry) {
	p.mdPath = firstNonEmpty(p.mdPath, "/proc/mdstat")
	p.psRoot = firstNonEmpty(p.psRoot, "/sys/class/power_supply")
	p.connCount = firstNonEmpty(p.connCount, "/proc/sys/net/netfilter/nf_conntrack_count")
	p.connMax = firstNonEmpty(p.connMax, "/proc/sys/net/netfilter/nf_conntrack_max")
	p.connStat = firstNonEmpty(p.connStat, "/proc/net/stat/nf_conntrack")
	p.softnet = firstNonEmpty(p.softnet, "/proc/net/softnet_stat")
	p.ipcShm = firstNonEmpty(p.ipcShm, "/proc/sysvipc/shm")
	p.ipcMsg = firstNonEmpty(p.ipcMsg, "/proc/sysvipc/msg")
	p.ipcSem = firstNonEmpty(p.ipcSem, "/proc/sysvipc/sem")
	p.mdSeen = map[string]bool{}
	p.psSeen = map[string]bool{}

	if _, err := readTrim(p.connCount); err == nil {
		p.haveConntrack = true
		nf := func(id, title, units string, prio int, dims ...*registry.Dimension) {
			ch := sysChart(id, "netfilter", title, units, prio, dims...)
			ch.Plugin, ch.Module = "proc", "netfilter"
			reg.AddChart(ch)
		}
		nf("netfilter.conntrack_sockets", "Connection tracker table", "connections", 8000,
			&registry.Dimension{ID: "connections"}, &registry.Dimension{ID: "max", Hidden: true})
		nf("netfilter.conntrack_new", "Connection tracker new connections", "connections/s", 8010,
			incDim("new"), incDim("ignore"), incDim("invalid"))
		nf("netfilter.conntrack_changes", "Connection tracker changes", "changes/s", 8020,
			incDim("insert"), incDim("insert_failed"), incDim("drop"), incDim("early_drop"))
		nf("netfilter.conntrack_expect", "Connection tracker expectations", "expectations/s", 8030,
			incDim("expect_new"), incDim("expect_create"), incDim("expect_delete"))
		nf("netfilter.conntrack_search", "Connection tracker searches", "searches/s", 8040,
			incDim("search_restart"))
		nf("netfilter.conntrack_errors", "Connection tracker errors", "errors/s", 8050,
			incDim("icmp_error"), incDim("insert_failed"), incDim("drop"))
	}
	if _, err := readTrim(p.softnet); err == nil {
		p.haveSoftnet = true
		ch := sysChart("system.softnet_stat", "softnet", "Softnet events", "events/s", 230,
			incDim("processed"), incDim("dropped"), incDim("squeezed"), incDim("received_rps"))
		reg.AddChart(ch)
	}
	if _, err := readTrim(p.ipcShm); err == nil {
		p.haveIPC = true
		reg.AddChart(sysChart("system.ipc_shared_mem_segs", "ipc", "IPC shared memory segments", "segments", 240,
			&registry.Dimension{ID: "segments"}))
		reg.AddChart(sysChart("system.ipc_shared_mem_size", "ipc", "IPC shared memory size", "bytes", 241,
			&registry.Dimension{ID: "size"}))
		reg.AddChart(sysChart("system.ipc_msq_queues", "ipc", "IPC message queues", "queues", 242,
			&registry.Dimension{ID: "queues"}, &registry.Dimension{ID: "messages"}))
		reg.AddChart(sysChart("system.ipc_semaphores", "ipc", "IPC semaphores", "semaphores", 243,
			&registry.Dimension{ID: "arrays"}, &registry.Dimension{ID: "semaphores"}))
	}
	if _, err := readTrim(p.mdPath); err == nil {
		p.haveMD = true
	}
	if ents, err := os.ReadDir(p.psRoot); err == nil && len(ents) > 0 {
		p.havePower = true
	}
}

func (p *procCollector) collectExtras(reg *registry.Registry, now time.Time) {
	if p.haveConntrack {
		p.collectConntrack(reg, now)
	}
	if p.haveSoftnet {
		if raw, err := readTrim(p.softnet); err == nil {
			st := parseSoftnet(raw)
			_ = reg.Collect("system.softnet_stat", now, map[string]float64{
				"processed": st.processed, "dropped": st.dropped, "squeezed": st.squeezed, "received_rps": st.receivedRPS})
		}
	}
	if p.haveIPC {
		p.collectIPC(reg, now)
	}
	if p.haveMD {
		if raw, err := readTrim(p.mdPath); err == nil {
			for _, a := range parseMDStat(raw) {
				p.ensureMDCharts(reg, a.Name)
				_ = reg.Collect("md.health."+a.Name, now, map[string]float64{"mismatch": a.Mismatch})
				_ = reg.Collect("md.disks."+a.Name, now, map[string]float64{"inuse": a.InUse, "down": a.Down, "total": a.Total})
				_ = reg.Collect("md.status."+a.Name, now, map[string]float64{"synced": a.Synced})
			}
		}
	}
	if p.havePower {
		for _, ps := range readPowerSupplies(p.psRoot) {
			p.ensurePSCharts(reg, ps)
			id := sanitizeID(ps.Name)
			_ = reg.Collect("powersupply.capacity."+id, now, map[string]float64{"capacity": ps.Capacity})
			_ = reg.Collect("powersupply.status."+id, now, powerStatusDims(ps.Status))
			if _, ok := reg.Chart("powersupply.voltage." + id); ok {
				_ = reg.Collect("powersupply.voltage."+id, now, map[string]float64{"voltage": ps.Voltage})
			}
		}
	}
}

func (p *procCollector) collectConntrack(reg *registry.Registry, now time.Time) {
	count, _ := readFloat(p.connCount)
	max, _ := readFloat(p.connMax)
	_ = reg.Collect("netfilter.conntrack_sockets", now, map[string]float64{"connections": count, "max": max})
	raw, err := readTrim(p.connStat)
	if err != nil {
		return
	}
	st := parseConntrackStat(raw)
	_ = reg.Collect("netfilter.conntrack_new", now, map[string]float64{"new": st["new"], "ignore": st["ignore"], "invalid": st["invalid"]})
	_ = reg.Collect("netfilter.conntrack_changes", now, map[string]float64{
		"insert": st["insert"], "insert_failed": st["insert_failed"], "drop": st["drop"], "early_drop": st["early_drop"]})
	_ = reg.Collect("netfilter.conntrack_expect", now, map[string]float64{
		"expect_new": st["expect_new"], "expect_create": st["expect_create"], "expect_delete": st["expect_delete"]})
	_ = reg.Collect("netfilter.conntrack_search", now, map[string]float64{"search_restart": st["search_restart"]})
	_ = reg.Collect("netfilter.conntrack_errors", now, map[string]float64{
		"icmp_error": st["icmp_error"], "insert_failed": st["insert_failed"], "drop": st["drop"]})
}

func (p *procCollector) collectIPC(reg *registry.Registry, now time.Time) {
	if raw, err := readTrim(p.ipcShm); err == nil {
		n, size := parseIPCTable(raw, "size")
		_ = reg.Collect("system.ipc_shared_mem_segs", now, map[string]float64{"segments": n})
		_ = reg.Collect("system.ipc_shared_mem_size", now, map[string]float64{"size": size})
	}
	if raw, err := readTrim(p.ipcMsg); err == nil {
		n, msgs := parseIPCTable(raw, "qnum")
		_ = reg.Collect("system.ipc_msq_queues", now, map[string]float64{"queues": n, "messages": msgs})
	}
	if raw, err := readTrim(p.ipcSem); err == nil {
		n, sems := parseIPCTable(raw, "nsems")
		_ = reg.Collect("system.ipc_semaphores", now, map[string]float64{"arrays": n, "semaphores": sems})
	}
}

func (p *procCollector) ensureMDCharts(reg *registry.Registry, name string) {
	if p.mdSeen[name] {
		return
	}
	p.mdSeen[name] = true
	lbl := map[string]string{"device": name}
	mk := func(suffix, title, units string, prio int, dims ...*registry.Dimension) {
		reg.AddChart(&registry.Chart{ID: "md." + suffix + "." + name, Context: "md." + suffix, Family: "md",
			Title: title + " " + name, Units: units, Priority: prio, Plugin: "proc", Module: "mdstat",
			Labels: lbl, Dimensions: dims})
	}
	mk("health", "MD RAID mismatch", "blocks", 9000, &registry.Dimension{ID: "mismatch"})
	mk("disks", "MD RAID disks", "disks", 9010, &registry.Dimension{ID: "inuse"}, &registry.Dimension{ID: "down"}, &registry.Dimension{ID: "total"})
	mk("status", "MD RAID synced", "percent", 9020, &registry.Dimension{ID: "synced"})
}

func (p *procCollector) ensurePSCharts(reg *registry.Registry, ps powerSupply) {
	id := sanitizeID(ps.Name)
	if p.psSeen[id] {
		return
	}
	p.psSeen[id] = true
	lbl := map[string]string{"device": ps.Name, "type": ps.Type}
	mk := func(suffix, title, units string, prio int, dims ...*registry.Dimension) {
		reg.AddChart(&registry.Chart{ID: "powersupply." + suffix + "." + id, Context: "powersupply." + suffix, Family: "power",
			Title: title + " " + ps.Name, Units: units, Priority: prio, Plugin: "proc", Module: "power_supply",
			Labels: lbl, Dimensions: dims})
	}
	mk("capacity", "Power supply capacity", "percentage", 9100, &registry.Dimension{ID: "capacity"})
	mk("status", "Power supply status", "boolean", 9110,
		&registry.Dimension{ID: "charging"}, &registry.Dimension{ID: "discharging"},
		&registry.Dimension{ID: "full"}, &registry.Dimension{ID: "not_charging"}, &registry.Dimension{ID: "unknown"})
	if ps.Voltage > 0 {
		mk("voltage", "Power supply voltage", "V", 9120, &registry.Dimension{ID: "voltage"})
	}
}

func firstNonEmpty(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

// parseConntrackStat sums per-CPU hex counters from /proc/net/stat/nf_conntrack.
// The `entries` column is global (repeated per CPU) and is ignored.
func parseConntrackStat(s string) map[string]float64 {
	out := map[string]float64{}
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) < 2 {
		return out
	}
	headers := strings.Fields(lines[0])
	for _, line := range lines[1:] {
		vals := strings.Fields(line)
		n := min(len(headers), len(vals))
		for i := 0; i < n; i++ {
			if headers[i] == "entries" {
				continue
			}
			out[headers[i]] += hexFloat(vals[i])
		}
	}
	return out
}

type softnetStat struct{ processed, dropped, squeezed, receivedRPS float64 }

func parseSoftnet(s string) softnetStat {
	var st softnetStat
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 {
			continue
		}
		st.processed += hexFloat(f[0])
		st.dropped += hexFloat(f[1])
		st.squeezed += hexFloat(f[2])
		if len(f) > 9 {
			st.receivedRPS += hexFloat(f[9])
		}
	}
	return st
}

func hexFloat(s string) float64 {
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil || len(b) == 0 {
		n, _ := strconv.ParseUint(s, 16, 64)
		return float64(n)
	}
	var n uint64
	for _, x := range b {
		n = n<<8 | uint64(x)
	}
	return float64(n)
}

// parseIPCTable skips the header row and returns (row count, sum of named column).
func parseIPCTable(s, sumCol string) (rows, sum float64) {
	sc := bufio.NewScanner(strings.NewReader(s))
	var idx = -1
	first := true
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		if first {
			first = false
			for i, h := range f {
				if strings.EqualFold(h, sumCol) {
					idx = i
					break
				}
			}
			continue
		}
		rows++
		if idx >= 0 && idx < len(f) {
			v, _ := strconv.ParseFloat(f[idx], 64)
			sum += v
		}
	}
	return
}

type mdArray struct {
	Name     string
	InUse    float64
	Total    float64
	Down     float64
	Synced   float64
	Mismatch float64
}

func parseMDStat(s string) []mdArray {
	var out []mdArray
	var cur *mdArray
	flush := func() {
		if cur != nil && cur.Name != "" {
			out = append(out, *cur)
		}
		cur = nil
	}
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "Personalities") || strings.HasPrefix(t, "unused devices"):
			flush()
		case strings.Contains(t, " : "):
			flush()
			name, _, _ := strings.Cut(t, " : ")
			cur = &mdArray{Name: strings.TrimSpace(name), Synced: 100}
		case cur != nil && strings.Contains(t, "["):
			// blocks line: "... [2/2] [UU]" or recovery "... resync = 30.1%"
			if i := strings.Index(t, "resync = "); i >= 0 {
				rest := t[i+len("resync = "):]
				pct, _, _ := strings.Cut(rest, "%")
				if v, err := strconv.ParseFloat(strings.TrimSpace(pct), 64); err == nil {
					cur.Synced = v
				}
			} else if i := strings.Index(t, "recovery = "); i >= 0 {
				rest := t[i+len("recovery = "):]
				pct, _, _ := strings.Cut(rest, "%")
				if v, err := strconv.ParseFloat(strings.TrimSpace(pct), 64); err == nil {
					cur.Synced = v
				}
			} else if i := strings.LastIndex(t, "["); i >= 0 {
				inner := strings.TrimSuffix(t[i+1:], "]")
				if strings.Contains(inner, "/") {
					a, b, _ := strings.Cut(inner, "/")
					cur.Total, _ = strconv.ParseFloat(b, 64)
					cur.InUse, _ = strconv.ParseFloat(a, 64)
					cur.Down = cur.Total - cur.InUse
				} else {
					// [UU_] bitmap
					cur.Total = float64(len(inner))
					for _, r := range inner {
						if r == 'U' {
							cur.InUse++
						} else {
							cur.Down++
						}
					}
				}
			}
		}
	}
	flush()
	return out
}

type powerSupply struct {
	Name, Type, Status string
	Capacity, Voltage  float64
}

func readPowerSupplies(root string) []powerSupply {
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []powerSupply
	for _, e := range ents {
		dir := filepath.Join(root, e.Name())
		ps := powerSupply{Name: e.Name()}
		if v, err := readTrim(filepath.Join(dir, "type")); err == nil {
			ps.Type = v
		}
		if v, err := readTrim(filepath.Join(dir, "status")); err == nil {
			ps.Status = v
		}
		if v, err := readFloat(filepath.Join(dir, "capacity")); err == nil {
			ps.Capacity = v
		}
		if v, err := readFloat(filepath.Join(dir, "voltage_now")); err == nil {
			ps.Voltage = v / 1e6
		}
		if ps.Type == "" && ps.Status == "" && ps.Capacity == 0 {
			continue
		}
		out = append(out, ps)
	}
	return out
}

func powerStatusDims(status string) map[string]float64 {
	out := map[string]float64{"charging": 0, "discharging": 0, "full": 0, "not_charging": 0, "unknown": 0}
	switch strings.ToLower(status) {
	case "charging":
		out["charging"] = 1
	case "discharging":
		out["discharging"] = 1
	case "full":
		out["full"] = 1
	case "not charging", "not_charging":
		out["not_charging"] = 1
	default:
		out["unknown"] = 1
	}
	return out
}
