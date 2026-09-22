package collect

import (
	"bytes"
	"context"
	"encoding/csv"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// perflibSnap is one sample of Windows Performance Counters, keyed as
// "object|instance|counter" (all lower-case). Instance is empty for
// singleton objects such as Memory. Values are already cooked (typeperf /
// Win32_PerfFormattedData), so charts use absolute algorithm.
type perflibSnap map[string]float64

func perflibKey(obj, inst, ctr string) string {
	return strings.ToLower(obj) + "|" + strings.ToLower(inst) + "|" + strings.ToLower(ctr)
}

func (s perflibSnap) get(obj, inst, ctr string) (float64, bool) {
	if s == nil {
		return 0, false
	}
	v, ok := s[perflibKey(obj, inst, ctr)]
	return v, ok
}

func (s perflibSnap) empty() bool { return len(s) == 0 }

func (s perflibSnap) instances(obj string) []string {
	obj = strings.ToLower(obj)
	seen := map[string]bool{}
	var out []string
	for k := range s {
		parts := strings.SplitN(k, "|", 3)
		if len(parts) != 3 || parts[0] != obj {
			continue
		}
		if !seen[parts[1]] {
			seen[parts[1]] = true
			out = append(out, parts[1])
		}
	}
	return out
}

func (w *windowsCollector) samplePerflib(ctx context.Context) (perflibSnap, error) {
	if w.perflib != nil {
		return w.perflib(ctx)
	}
	if runtime.GOOS != "windows" {
		return nil, nil
	}
	return w.livePerflib(ctx)
}

// perflibObjects is queried one wildcard at a time so a missing role
// (IIS, Hyper-V, AD…) does not fail the whole sample.
var perflibObjects = []string{
	`\System\*`,
	`\Memory\*`,
	`\Objects\*`,
	`\Processor(_Total)\*`,
	`\LogicalDisk(*)\*`,
	`\PhysicalDisk(*)\*`,
	`\Network Interface(*)\*`,
	`\Web Service(*)\*`,
	`\APP_POOL_WAS(*)\*`,
	`\ASP.NET\*`,
	`\ASP.NET Applications(*)\*`,
	`\.NET CLR Exceptions(*)\*`,
	`\.NET CLR LocksAndThreads(*)\*`,
	`\.NET CLR Memory(*)\*`,
	`\Hyper-V Hypervisor Virtual Processor(*)\*`,
	`\Hyper-V Dynamic Memory VM(*)\*`,
	`\SMB Server Shares(*)\*`,
	`\Thermal Zone Information(*)\*`,
	`\NUMA Node Memory(*)\*`,
	`\NTDS\*`,
	`\DirectoryServices(*)\*`,
	`\Certification Authority(*)\*`,
	`\AD FS\*`,
	`\MSExchangeTransport Queues(*)\*`,
	`\MSExchange RpcClientAccess\*`,
	`\Terminal Services\*`,
}

func (w *windowsCollector) livePerflib(ctx context.Context) (perflibSnap, error) {
	s := perflibSnap{}
	// typeperf -sc 1 waits ~1s per object. Sequential scans of the
	// ~25-object catalog stall Init/Collect (~25s) and Windows CI smoke.
	// WMI Win32_PerfFormattedData_* is the live path; typeperf remains
	// for fixtures and an explicit typeperf_scan opt-in.
	if w.cfg.TypeperfScan {
		w.scanTypeperf(ctx, s)
	}
	wmiFillPerflib(s)
	if s.empty() {
		return nil, nil
	}
	return s, nil
}

func (w *windowsCollector) scanTypeperf(ctx context.Context, s perflibSnap) {
	run := w.run
	if run == nil {
		run = execRun(w.cfg.Timeout)
	}
	cmd := w.cfg.Typeperf
	if cmd == "" {
		cmd = "typeperf"
	}
	for _, obj := range perflibObjects {
		if ctx.Err() != nil {
			break
		}
		out, err := run(ctx, cmd, "-sc", "1", obj)
		if err != nil || len(out) == 0 {
			continue
		}
		parseTypeperfCSV(s, out)
	}
}

func parseTypeperfCSV(dst perflibSnap, raw []byte) {
	if dst == nil {
		return
	}
	r := csv.NewReader(bytes.NewReader(raw))
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil || len(rows) < 2 {
		parsePerflibLines(dst, raw)
		return
	}
	header := rows[0]
	var data []string
	for i := 1; i < len(rows); i++ {
		if len(rows[i]) >= 2 {
			data = rows[i]
		}
	}
	if data == nil {
		return
	}
	for i := 1; i < len(header) && i < len(data); i++ {
		obj, inst, ctr := splitPerfPath(header[i])
		if ctr == "" {
			continue
		}
		v, ok := parsePerfNumber(data[i])
		if !ok {
			continue
		}
		dst[perflibKey(obj, inst, ctr)] = v
	}
}

func parsePerflibLines(dst perflibSnap, raw []byte) {
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "(") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		obj, inst, ctr := splitPerfPath(strings.TrimSpace(k))
		n, ok := parsePerfNumber(strings.TrimSpace(v))
		if !ok || ctr == "" {
			continue
		}
		dst[perflibKey(obj, inst, ctr)] = n
	}
}

func parsePerfNumber(s string) (float64, bool) {
	s = strings.TrimSpace(strings.Trim(s, `"`))
	if s == "" || strings.EqualFold(s, "n/a") || s == "-1" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

func splitPerfPath(p string) (obj, inst, ctr string) {
	p = strings.TrimSpace(strings.Trim(p, `"`))
	if strings.HasPrefix(p, `\\`) {
		rest := p[2:]
		if i := strings.Index(rest, `\`); i >= 0 {
			p = rest[i+1:]
		}
	}
	p = strings.TrimPrefix(p, `\`)
	i := strings.LastIndex(p, `\`)
	if i < 0 {
		return "", "", strings.ToLower(p)
	}
	left, ctr := p[:i], p[i+1:]
	if a := strings.Index(left, "("); a >= 0 && strings.HasSuffix(left, ")") {
		obj, inst = left[:a], left[a+1:len(left)-1]
	} else {
		obj, inst = left, ""
	}
	return strings.ToLower(strings.TrimSpace(obj)), strings.ToLower(strings.TrimSpace(inst)), strings.ToLower(strings.TrimSpace(ctr))
}

func (w *windowsCollector) winAdd(reg *registry.Registry, ch *registry.Chart) {
	if _, ok := reg.Chart(ch.ID); ok {
		return
	}
	if ch.Plugin == "" {
		ch.Plugin = "windows"
	}
	if ch.Module == "" {
		ch.Module = "perflib"
	}
	reg.AddChart(ch)
}

func (w *windowsCollector) winEnsure(reg *registry.Registry, id, context, family, title, units string, prio int, dims []*registry.Dimension, labels map[string]string) {
	if w.seen[id] {
		return
	}
	if _, ok := reg.Chart(id); ok {
		w.seen[id] = true
		return
	}
	w.seen[id] = true
	w.winAdd(reg, &registry.Chart{
		ID: id, Context: context, Family: family, Title: title, Units: units,
		Priority: prio, Dimensions: dims, Labels: labels, Plugin: "windows", Module: "perflib",
	})
}

func (w *windowsCollector) applyPerflib(reg *registry.Registry, s perflibSnap) {
	if s.empty() {
		return
	}
	if _, ok := s.get("system", "", "processor queue length"); ok {
		w.winEnsure(reg, "system.cpu_queue", "system.cpu_queue", "cpu", "Processor queue length", "processes",
			21050, []*registry.Dimension{{ID: "load"}}, nil)
	}
	if _, ok := s.get("processor", "_total", "interrupts/sec"); ok {
		w.winEnsure(reg, "system.intr", "system.intr", "cpu", "CPU interrupts", "interrupts/s",
			212, []*registry.Dimension{{ID: "interrupts"}}, nil)
	}
	w.ensureMem(reg, s)
	w.ensureObjects(reg, s)
	w.ensureDisks(reg, s)
	w.ensureNets(reg, s)
	w.ensureIIS(reg, s)
	w.ensureIISAppPool(reg, s)
	w.ensureASP(reg, s)
	w.ensureNetFramework(reg, s)
	w.ensureHyperV(reg, s)
	w.ensureSMB(reg, s)
	w.ensureThermal(reg, s)
	w.ensureNUMA(reg, s)
	w.ensureAD(reg, s)
	w.ensureADCS(reg, s)
	w.ensureADFS(reg, s)
	w.ensureExchange(reg, s)
	w.ensureTerminal(reg, s)
	w.ensureBattery(reg, s)
	w.ensureSensors(reg, s)
}

func (w *windowsCollector) collectPerflib(reg *registry.Registry, now time.Time, s perflibSnap) {
	if v, ok := s.get("system", "", "processor queue length"); ok {
		_ = reg.Collect("system.cpu_queue", now, map[string]float64{"load": v})
	}
	if v, ok := s.get("processor", "_total", "interrupts/sec"); ok {
		_ = reg.Collect("system.intr", now, map[string]float64{"interrupts": v})
	}
	w.collectMem(reg, now, s)
	w.collectObjects(reg, now, s)
	w.collectDisks(reg, now, s)
	w.collectNets(reg, now, s)
	w.collectIIS(reg, now, s)
	w.collectIISAppPool(reg, now, s)
	w.collectASP(reg, now, s)
	w.collectNetFramework(reg, now, s)
	w.collectHyperV(reg, now, s)
	w.collectSMB(reg, now, s)
	w.collectThermal(reg, now, s)
	w.collectNUMA(reg, now, s)
	w.collectAD(reg, now, s)
	w.collectADCS(reg, now, s)
	w.collectADFS(reg, now, s)
	w.collectExchange(reg, now, s)
	w.collectTerminal(reg, now, s)
	w.collectBattery(reg, now, s)
	w.collectSensors(reg, now, s)
}

func (w *windowsCollector) ensureMem(reg *registry.Registry, s perflibSnap) {
	if _, ok := s.get("memory", "", "pool paged bytes"); ok {
		w.winEnsure(reg, "mem.system_pool_size", "mem.system_pool_size", "mem", "Kernel pool size", "MiB",
			3900, []*registry.Dimension{{ID: "paged", Divisor: 1024 * 1024}, {ID: "nonpaged", Divisor: 1024 * 1024}}, nil)
	}
	if _, ok := s.get("memory", "", "pages input/sec"); ok {
		w.winEnsure(reg, "mem.swapio", "mem.swapio", "swap", "Swap I/O", "pages/s",
			502, []*registry.Dimension{{ID: "in"}, {ID: "out"}}, nil)
	}
	if _, ok := s.get("memory", "", "page faults/sec"); ok {
		w.winEnsure(reg, "mem.page_faults_breakdown", "mem.page_faults_breakdown", "pagecache", "Page faults", "faults/s",
			3910, []*registry.Dimension{{ID: "faults"}}, nil)
	}
	if _, ok := s.get("memory", "", "cache bytes"); ok {
		w.winEnsure(reg, "mem.system_cache", "mem.system_cache", "mem", "System cache", "MiB",
			3920, []*registry.Dimension{{ID: "cache", Divisor: 1024 * 1024}}, nil)
	}
	if _, ok := s.get("memory", "", "committed bytes"); ok {
		if _, exists := reg.Chart("mem.committed"); !exists {
			w.winEnsure(reg, "mem.committed", "mem.committed", "mem", "Committed memory", "bytes",
				3905, []*registry.Dimension{{ID: "committed"}, {ID: "limit"}}, nil)
		}
	}
}

func (w *windowsCollector) collectMem(reg *registry.Registry, now time.Time, s perflibSnap) {
	if _, ok := reg.Chart("mem.system_pool_size"); ok {
		paged, _ := s.get("memory", "", "pool paged bytes")
		non, _ := s.get("memory", "", "pool nonpaged bytes")
		_ = reg.Collect("mem.system_pool_size", now, map[string]float64{"paged": paged, "nonpaged": non})
	}
	if _, ok := reg.Chart("mem.swapio"); ok {
		in, _ := s.get("memory", "", "pages input/sec")
		out, _ := s.get("memory", "", "pages output/sec")
		_ = reg.Collect("mem.swapio", now, map[string]float64{"in": in, "out": out})
	}
	if v, ok := s.get("memory", "", "page faults/sec"); ok {
		_ = reg.Collect("mem.page_faults_breakdown", now, map[string]float64{"faults": v})
	}
	if v, ok := s.get("memory", "", "cache bytes"); ok {
		_ = reg.Collect("mem.system_cache", now, map[string]float64{"cache": v})
	}
	if _, ok := reg.Chart("mem.committed"); ok {
		c, _ := s.get("memory", "", "committed bytes")
		lim, _ := s.get("memory", "", "commit limit")
		_ = reg.Collect("mem.committed", now, map[string]float64{"committed": c, "limit": lim})
	}
}

func (w *windowsCollector) ensureObjects(reg *registry.Registry, s perflibSnap) {
	if _, ok := s.get("objects", "", "mutexes"); ok {
		w.winEnsure(reg, "system.ipc_mutexes", "system.ipc_mutexes", "ipc", "IPC mutexes", "mutexes",
			5005, []*registry.Dimension{{ID: "mutexes"}}, nil)
	}
	if _, ok := s.get("objects", "", "semaphores"); ok {
		if _, exists := reg.Chart("system.ipc_semaphores"); !exists {
			w.winEnsure(reg, "system.ipc_semaphores", "system.ipc_semaphores", "ipc", "IPC semaphores", "semaphores",
				5000, []*registry.Dimension{{ID: "semaphores"}}, nil)
		}
	}
	if _, ok := s.get("objects", "", "events"); ok {
		w.winEnsure(reg, "windows.objects.events", "windows.objects.events", "ipc", "Kernel events", "events",
			5006, []*registry.Dimension{{ID: "events"}}, nil)
	}
}

func (w *windowsCollector) collectObjects(reg *registry.Registry, now time.Time, s perflibSnap) {
	if v, ok := s.get("objects", "", "mutexes"); ok {
		_ = reg.Collect("system.ipc_mutexes", now, map[string]float64{"mutexes": v})
	}
	if v, ok := s.get("objects", "", "semaphores"); ok {
		_ = reg.Collect("system.ipc_semaphores", now, map[string]float64{"semaphores": v})
	}
	if v, ok := s.get("objects", "", "events"); ok {
		_ = reg.Collect("windows.objects.events", now, map[string]float64{"events": v})
	}
}

func skipDiskInst(inst string) bool {
	return inst == "" || inst == "_total"
}

func (w *windowsCollector) ensureDisks(reg *registry.Registry, s perflibSnap) {
	for _, inst := range s.instances("logicaldisk") {
		if skipDiskInst(inst) {
			continue
		}
		id := sanitizeID(inst)
		lbl := map[string]string{"device": inst}
		w.winEnsure(reg, "windows.logical_disk.io."+id, "windows.logical_disk.io", "disk", "Logical disk "+inst+" I/O", "KiB/s",
			20200, []*registry.Dimension{{ID: "read", Divisor: 1024}, {ID: "write", Divisor: 1024}}, lbl)
		w.winEnsure(reg, "windows.logical_disk.ops."+id, "windows.logical_disk.ops", "disk", "Logical disk "+inst+" operations", "ops/s",
			20210, []*registry.Dimension{{ID: "reads"}, {ID: "writes"}}, lbl)
		if _, ok := s.get("logicaldisk", inst, "% free space"); ok {
			w.winEnsure(reg, "windows.logical_disk.space."+id, "windows.logical_disk.space", "disk", "Logical disk "+inst+" free space", "%",
				20220, []*registry.Dimension{{ID: "avail"}}, lbl)
		}
	}
	for _, inst := range s.instances("physicaldisk") {
		if skipDiskInst(inst) {
			continue
		}
		id := sanitizeID(inst)
		lbl := map[string]string{"device": inst}
		w.winEnsure(reg, "windows.physical_disk.io."+id, "windows.physical_disk.io", "disk", "Physical disk "+inst+" I/O", "KiB/s",
			20100, []*registry.Dimension{{ID: "read", Divisor: 1024}, {ID: "write", Divisor: 1024}}, lbl)
		w.winEnsure(reg, "windows.physical_disk.ops."+id, "windows.physical_disk.ops", "disk", "Physical disk "+inst+" operations", "ops/s",
			20110, []*registry.Dimension{{ID: "reads"}, {ID: "writes"}}, lbl)
		w.winEnsure(reg, "windows.physical_disk.util."+id, "windows.physical_disk.util", "disk", "Physical disk "+inst+" utilization", "%",
			20120, []*registry.Dimension{{ID: "util"}}, lbl)
	}
}

func (w *windowsCollector) collectDisks(reg *registry.Registry, now time.Time, s perflibSnap) {
	for _, inst := range s.instances("logicaldisk") {
		if skipDiskInst(inst) {
			continue
		}
		id := sanitizeID(inst)
		r, _ := s.get("logicaldisk", inst, "disk read bytes/sec")
		wr, _ := s.get("logicaldisk", inst, "disk write bytes/sec")
		_ = reg.Collect("windows.logical_disk.io."+id, now, map[string]float64{"read": r, "write": wr})
		rs, _ := s.get("logicaldisk", inst, "disk reads/sec")
		ws, _ := s.get("logicaldisk", inst, "disk writes/sec")
		_ = reg.Collect("windows.logical_disk.ops."+id, now, map[string]float64{"reads": rs, "writes": ws})
		if v, ok := s.get("logicaldisk", inst, "% free space"); ok {
			_ = reg.Collect("windows.logical_disk.space."+id, now, map[string]float64{"avail": v})
		}
	}
	for _, inst := range s.instances("physicaldisk") {
		if skipDiskInst(inst) {
			continue
		}
		id := sanitizeID(inst)
		r, _ := s.get("physicaldisk", inst, "disk read bytes/sec")
		wr, _ := s.get("physicaldisk", inst, "disk write bytes/sec")
		_ = reg.Collect("windows.physical_disk.io."+id, now, map[string]float64{"read": r, "write": wr})
		rs, _ := s.get("physicaldisk", inst, "disk reads/sec")
		ws, _ := s.get("physicaldisk", inst, "disk writes/sec")
		_ = reg.Collect("windows.physical_disk.ops."+id, now, map[string]float64{"reads": rs, "writes": ws})
		if v, ok := s.get("physicaldisk", inst, "% disk time"); ok {
			_ = reg.Collect("windows.physical_disk.util."+id, now, map[string]float64{"util": v})
		}
	}
}

func (w *windowsCollector) ensureNets(reg *registry.Registry, s perflibSnap) {
	for _, inst := range s.instances("network interface") {
		if skipDiskInst(inst) {
			continue
		}
		id := sanitizeID(inst)
		lbl := map[string]string{"device": inst}
		w.winEnsure(reg, "windows.net.traffic."+id, "windows.net.traffic", "net", "Network "+inst+" traffic", "kilobits/s",
			7000, []*registry.Dimension{{ID: "received", Divisor: 1000 / 8}, {ID: "sent", Divisor: 1000 / 8, Multiplier: -1}}, lbl)
		w.winEnsure(reg, "windows.net.packets."+id, "windows.net.packets", "net", "Network "+inst+" packets", "packets/s",
			7010, []*registry.Dimension{{ID: "received"}, {ID: "sent", Multiplier: -1}}, lbl)
		w.winEnsure(reg, "windows.net.errors."+id, "windows.net.errors", "net", "Network "+inst+" errors", "errors/s",
			7020, []*registry.Dimension{{ID: "inbound"}, {ID: "outbound"}}, lbl)
	}
}

func (w *windowsCollector) collectNets(reg *registry.Registry, now time.Time, s perflibSnap) {
	for _, inst := range s.instances("network interface") {
		if skipDiskInst(inst) {
			continue
		}
		id := sanitizeID(inst)
		rx, _ := s.get("network interface", inst, "bytes received/sec")
		tx, _ := s.get("network interface", inst, "bytes sent/sec")
		_ = reg.Collect("windows.net.traffic."+id, now, map[string]float64{"received": rx, "sent": tx})
		pr, _ := s.get("network interface", inst, "packets received/sec")
		ps, _ := s.get("network interface", inst, "packets sent/sec")
		_ = reg.Collect("windows.net.packets."+id, now, map[string]float64{"received": pr, "sent": ps})
		er, _ := s.get("network interface", inst, "packets received errors")
		es, _ := s.get("network interface", inst, "packets outbound errors")
		_ = reg.Collect("windows.net.errors."+id, now, map[string]float64{"inbound": er, "outbound": es})
	}
}

func (w *windowsCollector) ensureIIS(reg *registry.Registry, s perflibSnap) {
	for _, inst := range s.instances("web service") {
		id := sanitizeID(firstNonEmpty(inst, "_total"))
		lbl := map[string]string{"website": inst}
		title := "IIS " + firstNonEmpty(inst, "_Total")
		w.winEnsure(reg, "iis.website_requests_rate."+id, "iis.website_requests_rate", "iis", title+" requests", "requests/s",
			21000, []*registry.Dimension{{ID: "requests"}}, lbl)
		w.winEnsure(reg, "iis.website_active_connections_count."+id, "iis.website_active_connections_count", "iis", title+" connections", "connections",
			21040, []*registry.Dimension{{ID: "connections"}}, lbl)
		w.winEnsure(reg, "iis.website_traffic."+id, "iis.website_traffic", "iis", title+" traffic", "KiB/s",
			21020, []*registry.Dimension{{ID: "received", Divisor: 1024}, {ID: "sent", Divisor: 1024}}, lbl)
		w.winEnsure(reg, "iis.website_errors_rate."+id, "iis.website_errors_rate", "iis", title+" errors", "errors/s",
			21060, []*registry.Dimension{{ID: "locked"}, {ID: "not_found"}}, lbl)
		w.winEnsure(reg, "iis.website_users_count."+id, "iis.website_users_count", "iis", title+" users", "users",
			21050, []*registry.Dimension{{ID: "anonymous"}, {ID: "nonanonymous"}}, lbl)
	}
}

func (w *windowsCollector) collectIIS(reg *registry.Registry, now time.Time, s perflibSnap) {
	for _, inst := range s.instances("web service") {
		id := sanitizeID(firstNonEmpty(inst, "_total"))
		req, _ := s.get("web service", inst, "total method requests/sec")
		if req == 0 {
			req, _ = s.get("web service", inst, "get requests/sec")
		}
		_ = reg.Collect("iis.website_requests_rate."+id, now, map[string]float64{"requests": req})
		conn, _ := s.get("web service", inst, "current connections")
		_ = reg.Collect("iis.website_active_connections_count."+id, now, map[string]float64{"connections": conn})
		rx, _ := s.get("web service", inst, "bytes received/sec")
		tx, _ := s.get("web service", inst, "bytes sent/sec")
		_ = reg.Collect("iis.website_traffic."+id, now, map[string]float64{"received": rx, "sent": tx})
		nf, _ := s.get("web service", inst, "not found errors/sec")
		lk, _ := s.get("web service", inst, "locked errors/sec")
		_ = reg.Collect("iis.website_errors_rate."+id, now, map[string]float64{"not_found": nf, "locked": lk})
		anon, _ := s.get("web service", inst, "current anonymous users")
		non, _ := s.get("web service", inst, "current nonanonymous users")
		_ = reg.Collect("iis.website_users_count."+id, now, map[string]float64{"anonymous": anon, "nonanonymous": non})
	}
}

func appPoolStateVals(state float64) map[string]float64 {
	vals := map[string]float64{
		"uninitialized": 0, "initialized": 0, "running": 0,
		"disabling": 0, "disabled": 0, "shutdown_pending": 0, "delete_pending": 0,
	}
	names := []string{"", "uninitialized", "initialized", "running", "disabling", "disabled", "shutdown_pending", "delete_pending"}
	i := int(state)
	if i >= 1 && i < len(names) {
		vals[names[i]] = 1
	}
	return vals
}

func (w *windowsCollector) ensureIISAppPool(reg *registry.Registry, s perflibSnap) {
	for _, inst := range s.instances("app_pool_was") {
		if skipDiskInst(inst) {
			continue
		}
		id := sanitizeID(inst)
		lbl := map[string]string{"app": inst}
		title := "IIS app pool " + inst
		w.winEnsure(reg, "iis.application_pool_current_status."+id, "iis.application_pool_current_status", "iis",
			title+" status", "status", 21100, []*registry.Dimension{
				{ID: "uninitialized"}, {ID: "initialized"}, {ID: "running"}, {ID: "disabling"},
				{ID: "disabled"}, {ID: "shutdown_pending"}, {ID: "delete_pending"},
			}, lbl)
		w.winEnsure(reg, "iis.application_pool_current_worker_processes."+id, "iis.application_pool_current_worker_processes", "iis",
			title+" worker processes", "processes", 21110, []*registry.Dimension{{ID: "running"}}, lbl)
		w.winEnsure(reg, "iis.application_pool_worker_processes_created."+id, "iis.application_pool_worker_processes_created", "iis",
			title+" workers created", "processes/s", 21120, []*registry.Dimension{incDim("created")}, lbl)
		w.winEnsure(reg, "iis.application_pool_maximum_worker_processes."+id, "iis.application_pool_maximum_worker_processes", "iis",
			title+" max workers", "processes", 21130, []*registry.Dimension{{ID: "created"}}, lbl)
		w.winEnsure(reg, "iis.application_pool_recent_worker_process_failures."+id, "iis.application_pool_recent_worker_process_failures", "iis",
			title+" recent failures", "failures/s", 21140, []*registry.Dimension{incDim("failures")}, lbl)
		w.winEnsure(reg, "iis.application_pool_worker_process_failures."+id, "iis.application_pool_worker_process_failures", "iis",
			title+" worker failures", "failures/s", 21150, []*registry.Dimension{
				incDim("crash"), incDim("ping"), incDim("startup"), incDim("shutdown"),
			}, lbl)
		w.winEnsure(reg, "iis.application_pool_recycles."+id, "iis.application_pool_recycles", "iis",
			title+" recycles", "recycles/s", 21160, []*registry.Dimension{incDim("recycles")}, lbl)
		w.winEnsure(reg, "iis.application_pool_uptime."+id, "iis.application_pool_uptime", "iis",
			title+" uptime", "seconds", 21170, []*registry.Dimension{{ID: "uptime"}}, lbl)
	}
}

func (w *windowsCollector) collectIISAppPool(reg *registry.Registry, now time.Time, s perflibSnap) {
	for _, inst := range s.instances("app_pool_was") {
		if skipDiskInst(inst) {
			continue
		}
		id := sanitizeID(inst)
		st, _ := s.get("app_pool_was", inst, "current application pool state")
		_ = reg.Collect("iis.application_pool_current_status."+id, now, appPoolStateVals(st))
		wp, _ := s.get("app_pool_was", inst, "current worker processes")
		_ = reg.Collect("iis.application_pool_current_worker_processes."+id, now, map[string]float64{"running": wp})
		created, _ := s.get("app_pool_was", inst, "total worker processes created")
		_ = reg.Collect("iis.application_pool_worker_processes_created."+id, now, map[string]float64{"created": created})
		maxw, _ := s.get("app_pool_was", inst, "maximum worker processes")
		_ = reg.Collect("iis.application_pool_maximum_worker_processes."+id, now, map[string]float64{"created": maxw})
		recent, _ := s.get("app_pool_was", inst, "recent worker process failures")
		_ = reg.Collect("iis.application_pool_recent_worker_process_failures."+id, now, map[string]float64{"failures": recent})
		crash, _ := s.get("app_pool_was", inst, "total worker process failures")
		ping, _ := s.get("app_pool_was", inst, "total worker process ping failures")
		start, _ := s.get("app_pool_was", inst, "total worker process startup failures")
		shut, _ := s.get("app_pool_was", inst, "total worker process shutdown failures")
		_ = reg.Collect("iis.application_pool_worker_process_failures."+id, now, map[string]float64{
			"crash": crash, "ping": ping, "startup": start, "shutdown": shut,
		})
		rec, _ := s.get("app_pool_was", inst, "total application pool recycles")
		_ = reg.Collect("iis.application_pool_recycles."+id, now, map[string]float64{"recycles": rec})
		up, _ := s.get("app_pool_was", inst, "current application pool uptime")
		_ = reg.Collect("iis.application_pool_uptime."+id, now, map[string]float64{"uptime": up})
	}
}

func (w *windowsCollector) ensureASP(reg *registry.Registry, s perflibSnap) {
	if _, ok := s.get("asp.net", "", "application restarts"); ok {
		w.winEnsure(reg, "aspnet.application_restarts", "aspnet.application_restarts", "aspnet", "ASP.NET application restarts", "restarts",
			22000, []*registry.Dimension{{ID: "restarts"}}, nil)
		w.winEnsure(reg, "aspnet.worker_process_restarts", "aspnet.worker_process_restarts", "aspnet", "ASP.NET worker process restarts", "restarts",
			22010, []*registry.Dimension{{ID: "restarts"}}, nil)
		w.winEnsure(reg, "aspnet.requests_executing", "aspnet.requests_executing", "aspnet", "ASP.NET requests executing", "requests",
			22020, []*registry.Dimension{{ID: "requests"}}, nil)
		w.winEnsure(reg, "aspnet.requests_in_application_queue", "aspnet.requests_in_application_queue", "aspnet", "ASP.NET queued requests", "requests",
			22030, []*registry.Dimension{{ID: "queued"}}, nil)
		w.winEnsure(reg, "aspnet.requests_failed", "aspnet.requests_failed", "aspnet", "ASP.NET failed requests", "requests",
			22040, []*registry.Dimension{{ID: "failed"}}, nil)
	}
	for _, inst := range s.instances("asp.net applications") {
		id := sanitizeID(firstNonEmpty(inst, "_total"))
		lbl := map[string]string{"app": inst}
		w.winEnsure(reg, "aspnet.sessions_active."+id, "aspnet.sessions_active", "aspnet", "ASP.NET "+inst+" sessions", "sessions",
			22100, []*registry.Dimension{{ID: "active"}}, lbl)
		w.winEnsure(reg, "aspnet.errors_during_execution."+id, "aspnet.errors_during_execution", "aspnet", "ASP.NET "+inst+" execution errors", "errors",
			22110, []*registry.Dimension{{ID: "errors"}}, lbl)
	}
}

func (w *windowsCollector) collectASP(reg *registry.Registry, now time.Time, s perflibSnap) {
	if v, ok := s.get("asp.net", "", "application restarts"); ok {
		_ = reg.Collect("aspnet.application_restarts", now, map[string]float64{"restarts": v})
	}
	if v, ok := s.get("asp.net", "", "worker process restarts"); ok {
		_ = reg.Collect("aspnet.worker_process_restarts", now, map[string]float64{"restarts": v})
	}
	if v, ok := s.get("asp.net", "", "requests executing"); ok {
		_ = reg.Collect("aspnet.requests_executing", now, map[string]float64{"requests": v})
	}
	if v, ok := s.get("asp.net", "", "requests in application queue"); ok {
		_ = reg.Collect("aspnet.requests_in_application_queue", now, map[string]float64{"queued": v})
	}
	if v, ok := s.get("asp.net", "", "requests failed"); ok {
		_ = reg.Collect("aspnet.requests_failed", now, map[string]float64{"failed": v})
	}
	for _, inst := range s.instances("asp.net applications") {
		id := sanitizeID(firstNonEmpty(inst, "_total"))
		if v, ok := s.get("asp.net applications", inst, "sessions active"); ok {
			_ = reg.Collect("aspnet.sessions_active."+id, now, map[string]float64{"active": v})
		}
		if v, ok := s.get("asp.net applications", inst, "errors during execution"); ok {
			_ = reg.Collect("aspnet.errors_during_execution."+id, now, map[string]float64{"errors": v})
		}
	}
}

func (w *windowsCollector) ensureNetFramework(reg *registry.Registry, s perflibSnap) {
	for _, inst := range s.instances(".net clr exceptions") {
		id := sanitizeID(firstNonEmpty(inst, "_global_"))
		lbl := map[string]string{"process": inst}
		w.winEnsure(reg, "netframework.clr_exceptions."+id, "netframework.clr_exceptions", "netframework", ".NET CLR "+inst+" exceptions", "exceptions/s",
			23000, []*registry.Dimension{{ID: "thrown"}}, lbl)
	}
	for _, inst := range s.instances(".net clr locksandthreads") {
		id := sanitizeID(firstNonEmpty(inst, "_global_"))
		lbl := map[string]string{"process": inst}
		w.winEnsure(reg, "netframework.clr_locks_queue."+id, "netframework.clr_locks_queue", "netframework", ".NET CLR "+inst+" lock queue", "threads",
			23010, []*registry.Dimension{{ID: "queue"}}, lbl)
		w.winEnsure(reg, "netframework.clr_threads."+id, "netframework.clr_threads", "netframework", ".NET CLR "+inst+" threads", "threads",
			23020, []*registry.Dimension{{ID: "current"}, {ID: "recognized"}}, lbl)
		w.winEnsure(reg, "netframework.clr_contentions."+id, "netframework.clr_contentions", "netframework", ".NET CLR "+inst+" contentions", "contentions/s",
			23030, []*registry.Dimension{{ID: "contentions"}}, lbl)
	}
}

func (w *windowsCollector) collectNetFramework(reg *registry.Registry, now time.Time, s perflibSnap) {
	for _, inst := range s.instances(".net clr exceptions") {
		id := sanitizeID(firstNonEmpty(inst, "_global_"))
		v, _ := s.get(".net clr exceptions", inst, "# of excep. thrown / sec")
		if v == 0 {
			v, _ = s.get(".net clr exceptions", inst, "# of exceptions thrown / sec")
		}
		_ = reg.Collect("netframework.clr_exceptions."+id, now, map[string]float64{"thrown": v})
	}
	for _, inst := range s.instances(".net clr locksandthreads") {
		id := sanitizeID(firstNonEmpty(inst, "_global_"))
		q, _ := s.get(".net clr locksandthreads", inst, "queue length / sec")
		_ = reg.Collect("netframework.clr_locks_queue."+id, now, map[string]float64{"queue": q})
		cur, _ := s.get(".net clr locksandthreads", inst, "# of current physical threads")
		rec, _ := s.get(".net clr locksandthreads", inst, "# of current recognized threads")
		_ = reg.Collect("netframework.clr_threads."+id, now, map[string]float64{"current": cur, "recognized": rec})
		c, _ := s.get(".net clr locksandthreads", inst, "contention rate / sec")
		_ = reg.Collect("netframework.clr_contentions."+id, now, map[string]float64{"contentions": c})
	}
}

func (w *windowsCollector) ensureHyperV(reg *registry.Registry, s perflibSnap) {
	for _, inst := range s.instances("hyper-v hypervisor virtual processor") {
		if strings.Contains(inst, "_total") {
			continue
		}
		vm := hypervVMName(inst)
		id := sanitizeID(vm)
		lbl := map[string]string{"vm": vm}
		w.winEnsure(reg, "hyperv.vm_cpu."+id, "hyperv.vm_cpu", "hyperv", "Hyper-V "+vm+" CPU", "%",
			24000, []*registry.Dimension{{ID: "guest"}, {ID: "hypervisor"}}, lbl)
	}
}

func hypervVMName(inst string) string {
	if i := strings.Index(inst, ":"); i > 0 {
		return strings.TrimSpace(inst[:i])
	}
	return inst
}

func (w *windowsCollector) collectHyperV(reg *registry.Registry, now time.Time, s perflibSnap) {
	byVM := map[string]map[string]float64{}
	for _, inst := range s.instances("hyper-v hypervisor virtual processor") {
		if strings.Contains(inst, "_total") {
			continue
		}
		vm := hypervVMName(inst)
		m := byVM[vm]
		if m == nil {
			m = map[string]float64{}
			byVM[vm] = m
		}
		g, _ := s.get("hyper-v hypervisor virtual processor", inst, "% guest run time")
		h, _ := s.get("hyper-v hypervisor virtual processor", inst, "% hypervisor run time")
		m["guest"] += g
		m["hypervisor"] += h
	}
	for vm, vals := range byVM {
		_ = reg.Collect("hyperv.vm_cpu."+sanitizeID(vm), now, vals)
	}
}

func (w *windowsCollector) ensureSMB(reg *registry.Registry, s perflibSnap) {
	for _, inst := range s.instances("smb server shares") {
		id := sanitizeID(inst)
		lbl := map[string]string{"share": inst}
		w.winEnsure(reg, "smb.server_shares_current_open_file_count."+id, "smb.server_shares_current_open_file_count", "smb",
			"SMB "+inst+" open files", "files", 25000, []*registry.Dimension{{ID: "open"}}, lbl)
		w.winEnsure(reg, "smb.server_shares_read_requests."+id, "smb.server_shares_read_requests", "smb",
			"SMB "+inst+" read requests", "requests/s", 25010, []*registry.Dimension{{ID: "reads"}}, lbl)
		w.winEnsure(reg, "smb.server_shares_write_requests."+id, "smb.server_shares_write_requests", "smb",
			"SMB "+inst+" write requests", "requests/s", 25020, []*registry.Dimension{{ID: "writes"}}, lbl)
	}
}

func (w *windowsCollector) collectSMB(reg *registry.Registry, now time.Time, s perflibSnap) {
	for _, inst := range s.instances("smb server shares") {
		id := sanitizeID(inst)
		o, _ := s.get("smb server shares", inst, "current open file count")
		_ = reg.Collect("smb.server_shares_current_open_file_count."+id, now, map[string]float64{"open": o})
		r, _ := s.get("smb server shares", inst, "read requests/sec")
		_ = reg.Collect("smb.server_shares_read_requests."+id, now, map[string]float64{"reads": r})
		wr, _ := s.get("smb server shares", inst, "write requests/sec")
		_ = reg.Collect("smb.server_shares_write_requests."+id, now, map[string]float64{"writes": wr})
	}
}

func (w *windowsCollector) ensureThermal(reg *registry.Registry, s perflibSnap) {
	for _, inst := range s.instances("thermal zone information") {
		id := sanitizeID(firstNonEmpty(inst, "zone"))
		w.winEnsure(reg, "system.thermalzone_temperature."+id, "system.thermalzone_temperature", "thermalzone",
			"Thermal zone "+inst, "Celsius", 26000, []*registry.Dimension{{ID: "temperature"}},
			map[string]string{"thermalzone": inst})
	}
}

func thermalCelsius(v float64) float64 {
	if v > 1000 { // ACPI tenths of Kelvin (MSAcpi_ThermalZoneTemperature)
		return v/10 - 273.15
	}
	if v > 200 { // Kelvin (Netdata Perflib Thermal Zone Information)
		return v - 273.15
	}
	return v
}

func (w *windowsCollector) collectThermal(reg *registry.Registry, now time.Time, s perflibSnap) {
	for _, inst := range s.instances("thermal zone information") {
		id := sanitizeID(firstNonEmpty(inst, "zone"))
		v, _ := s.get("thermal zone information", inst, "temperature")
		_ = reg.Collect("system.thermalzone_temperature."+id, now, map[string]float64{"temperature": thermalCelsius(v)})
	}
}

func (w *windowsCollector) ensureNUMA(reg *registry.Registry, s perflibSnap) {
	for _, inst := range s.instances("numa node memory") {
		if inst == "_total" {
			continue
		}
		id := sanitizeID(inst)
		w.winEnsure(reg, "mem.numa_node_mem_usage."+id, "mem.numa_node_mem_usage", "numa",
			"NUMA node "+inst+" memory", "MiB", 27000,
			[]*registry.Dimension{{ID: "free"}, {ID: "standby"}},
			map[string]string{"node": inst})
	}
}

func (w *windowsCollector) collectNUMA(reg *registry.Registry, now time.Time, s perflibSnap) {
	for _, inst := range s.instances("numa node memory") {
		if inst == "_total" {
			continue
		}
		id := sanitizeID(inst)
		free, _ := s.get("numa node memory", inst, "free & zero page list mbytes")
		stby, _ := s.get("numa node memory", inst, "standby list mbytes")
		_ = reg.Collect("mem.numa_node_mem_usage."+id, now, map[string]float64{"free": free, "standby": stby})
	}
}

func (w *windowsCollector) ensureAD(reg *registry.Registry, s perflibSnap) {
	if _, ok := s.get("ntds", "", "ds directory reads/sec"); ok {
		w.winEnsure(reg, "ad.directory_operations", "ad.directory_operations", "ad", "AD directory operations", "ops/s",
			28000, []*registry.Dimension{{ID: "reads"}, {ID: "writes"}, {ID: "searches"}}, nil)
		w.winEnsure(reg, "ad.ldap_client_sessions", "ad.ldap_client_sessions", "ad", "AD LDAP client sessions", "sessions",
			28010, []*registry.Dimension{{ID: "sessions"}}, nil)
		w.winEnsure(reg, "ad.ldap_searches", "ad.ldap_searches", "ad", "AD LDAP searches", "searches/s",
			28020, []*registry.Dimension{{ID: "searches"}}, nil)
	}
}

func (w *windowsCollector) collectAD(reg *registry.Registry, now time.Time, s perflibSnap) {
	if _, ok := reg.Chart("ad.directory_operations"); !ok {
		return
	}
	r, _ := s.get("ntds", "", "ds directory reads/sec")
	wr, _ := s.get("ntds", "", "ds directory writes/sec")
	se, _ := s.get("ntds", "", "ds directory searches/sec")
	_ = reg.Collect("ad.directory_operations", now, map[string]float64{"reads": r, "writes": wr, "searches": se})
	if v, ok := s.get("ntds", "", "ldap client sessions"); ok {
		_ = reg.Collect("ad.ldap_client_sessions", now, map[string]float64{"sessions": v})
	}
	if v, ok := s.get("ntds", "", "ldap searches/sec"); ok {
		_ = reg.Collect("ad.ldap_searches", now, map[string]float64{"searches": v})
	}
}

func (w *windowsCollector) ensureADCS(reg *registry.Registry, s perflibSnap) {
	for _, inst := range s.instances("certification authority") {
		id := sanitizeID(inst)
		lbl := map[string]string{"ca": inst}
		w.winEnsure(reg, "adcs.cert_requests."+id, "adcs.cert_requests", "adcs", "ADCS "+inst+" requests", "requests/s",
			28100, []*registry.Dimension{{ID: "requests"}}, lbl)
		w.winEnsure(reg, "adcs.cert_failed."+id, "adcs.cert_failed", "adcs", "ADCS "+inst+" failed requests", "requests/s",
			28110, []*registry.Dimension{{ID: "failed"}}, lbl)
	}
}

func (w *windowsCollector) collectADCS(reg *registry.Registry, now time.Time, s perflibSnap) {
	for _, inst := range s.instances("certification authority") {
		id := sanitizeID(inst)
		r, _ := s.get("certification authority", inst, "requests/sec")
		_ = reg.Collect("adcs.cert_requests."+id, now, map[string]float64{"requests": r})
		f, _ := s.get("certification authority", inst, "failed requests/sec")
		_ = reg.Collect("adcs.cert_failed."+id, now, map[string]float64{"failed": f})
	}
}

func (w *windowsCollector) ensureADFS(reg *registry.Registry, s perflibSnap) {
	if _, ok := s.get("ad fs", "", "sso authentications/sec"); ok {
		w.winEnsure(reg, "adfs.sso_auth", "adfs.sso_auth", "adfs", "AD FS SSO authentications", "auths/s",
			28200, []*registry.Dimension{{ID: "sso"}}, nil)
		w.winEnsure(reg, "adfs.login_failures", "adfs.login_failures", "adfs", "AD FS login failures", "failures/s",
			28210, []*registry.Dimension{{ID: "failures"}}, nil)
	}
}

func (w *windowsCollector) collectADFS(reg *registry.Registry, now time.Time, s perflibSnap) {
	if v, ok := s.get("ad fs", "", "sso authentications/sec"); ok {
		_ = reg.Collect("adfs.sso_auth", now, map[string]float64{"sso": v})
	}
	fail, _ := s.get("ad fs", "", "extranet account lockouts/sec")
	if fail == 0 {
		fail, _ = s.get("ad fs", "", "failed authentications/sec")
	}
	if _, ok := reg.Chart("adfs.login_failures"); ok {
		_ = reg.Collect("adfs.login_failures", now, map[string]float64{"failures": fail})
	}
}

func (w *windowsCollector) ensureExchange(reg *registry.Registry, s perflibSnap) {
	for _, inst := range s.instances("msexchangetransport queues") {
		id := sanitizeID(firstNonEmpty(inst, "_total"))
		w.winEnsure(reg, "exchange.transport_queues."+id, "exchange.transport_queues", "exchange",
			"Exchange transport queues "+inst, "messages", 28300,
			[]*registry.Dimension{{ID: "active"}, {ID: "retry"}, {ID: "unreachable"}, {ID: "poison"}},
			map[string]string{"queue": inst})
	}
	if _, ok := s.get("msexchange rpcclientaccess", "", "rpc requests"); ok {
		w.winEnsure(reg, "exchange.rpc_requests", "exchange.rpc_requests", "exchange", "Exchange RPC requests", "requests",
			28310, []*registry.Dimension{{ID: "requests"}}, nil)
	}
}

func (w *windowsCollector) collectExchange(reg *registry.Registry, now time.Time, s perflibSnap) {
	for _, inst := range s.instances("msexchangetransport queues") {
		id := sanitizeID(firstNonEmpty(inst, "_total"))
		a, _ := s.get("msexchangetransport queues", inst, "active mailbox delivery queue length")
		r, _ := s.get("msexchangetransport queues", inst, "retry mailbox delivery queue length")
		u, _ := s.get("msexchangetransport queues", inst, "unreachable queue length")
		p, _ := s.get("msexchangetransport queues", inst, "poison queue length")
		_ = reg.Collect("exchange.transport_queues."+id, now, map[string]float64{"active": a, "retry": r, "unreachable": u, "poison": p})
	}
	if v, ok := s.get("msexchange rpcclientaccess", "", "rpc requests"); ok {
		_ = reg.Collect("exchange.rpc_requests", now, map[string]float64{"requests": v})
	}
}

func (w *windowsCollector) ensureTerminal(reg *registry.Registry, s perflibSnap) {
	if _, ok := s.get("terminal services", "", "active sessions"); ok {
		w.winEnsure(reg, "windows.terminal_services.sessions", "windows.terminal_services.sessions", "terminal_services",
			"Terminal Services sessions", "sessions", 28400,
			[]*registry.Dimension{{ID: "active"}, {ID: "inactive"}}, nil)
	}
}

func (w *windowsCollector) collectTerminal(reg *registry.Registry, now time.Time, s perflibSnap) {
	if _, ok := reg.Chart("windows.terminal_services.sessions"); !ok {
		return
	}
	a, _ := s.get("terminal services", "", "active sessions")
	in, _ := s.get("terminal services", "", "inactive sessions")
	_ = reg.Collect("windows.terminal_services.sessions", now, map[string]float64{"active": a, "inactive": in})
}

func (w *windowsCollector) ensureBattery(reg *registry.Registry, s perflibSnap) {
	if _, ok := s.get("battery", "_total", "estimatedchargeremaining"); ok {
		w.winEnsure(reg, "windows.power.charge", "windows.power.charge", "power", "Battery charge remaining", "%",
			28500, []*registry.Dimension{{ID: "charge"}}, nil)
		w.winEnsure(reg, "powersupply.capacity", "powersupply.capacity", "power", "Battery capacity", "percentage",
			28501, []*registry.Dimension{{ID: "capacity"}}, nil)
	}
	if _, ok := s.get("battery", "_total", "designvoltage"); ok {
		w.winEnsure(reg, "powersupply.voltage", "powersupply.voltage", "power", "Power supply voltage", "V",
			28502, []*registry.Dimension{{ID: "now", Divisor: 1000}}, nil)
	}
}

func (w *windowsCollector) collectBattery(reg *registry.Registry, now time.Time, s perflibSnap) {
	if v, ok := s.get("battery", "_total", "estimatedchargeremaining"); ok {
		_ = reg.Collect("windows.power.charge", now, map[string]float64{"charge": v})
		_ = reg.Collect("powersupply.capacity", now, map[string]float64{"capacity": v})
	}
	if v, ok := s.get("battery", "_total", "designvoltage"); ok {
		_ = reg.Collect("powersupply.voltage", now, map[string]float64{"now": v})
	}
}

type winSensorReading struct {
	id, name string
	celsius  float64
}

func perflibSensorReadings(s perflibSnap) []winSensorReading {
	var out []winSensorReading
	add := func(obj, ctr string) {
		for _, inst := range s.instances(obj) {
			v, ok := s.get(obj, inst, ctr)
			if !ok || v == 0 {
				continue
			}
			name := firstNonEmpty(inst, obj)
			out = append(out, winSensorReading{id: sanitizeID(name), name: name, celsius: thermalCelsius(v)})
		}
	}
	add("msacpi_thermalzonetemperature", "currenttemperature")
	add("win32_temperatureprobe", "currentreading")
	add("thermal zone information", "temperature")
	return out
}

var sensorHistBounds = []float64{40, 50, 60, 70, 80, 85, 90, 95, 100}

func sensorHistogramDims() []*registry.Dimension {
	dims := make([]*registry.Dimension, 0, len(sensorHistBounds)+1)
	for _, b := range sensorHistBounds {
		dims = append(dims, &registry.Dimension{ID: strconv.FormatFloat(b, 'f', 0, 64)})
	}
	dims = append(dims, &registry.Dimension{ID: "+Inf"})
	return dims
}

func sensorHistogramVals(temps []winSensorReading) map[string]float64 {
	vals := map[string]float64{"+Inf": 0}
	for _, b := range sensorHistBounds {
		vals[strconv.FormatFloat(b, 'f', 0, 64)] = 0
	}
	for _, t := range temps {
		placed := false
		for _, b := range sensorHistBounds {
			if t.celsius <= b {
				vals[strconv.FormatFloat(b, 'f', 0, 64)]++
				placed = true
				break
			}
		}
		if !placed {
			vals["+Inf"]++
		}
	}
	return vals
}

func (w *windowsCollector) ensureSensors(reg *registry.Registry, s perflibSnap) {
	temps := perflibSensorReadings(s)
	if len(temps) == 0 {
		return
	}
	cpuDims := make([]*registry.Dimension, 0, len(temps))
	seen := map[string]bool{}
	for _, t := range temps {
		if seen[t.id] {
			continue
		}
		seen[t.id] = true
		lbl := map[string]string{"label": t.name}
		w.winEnsure(reg, "system.hw.sensor.temperature.input."+t.id, "system.hw.sensor.temperature.input", "sensors",
			"Sensor "+t.name+" temperature", "Cel", 28610, []*registry.Dimension{{ID: "input"}}, lbl)
		cpuDims = append(cpuDims, &registry.Dimension{ID: t.id, Name: t.name})
	}
	w.winEnsure(reg, "system.hw.sensor.temperature.histogram", "system.hw.sensor.temperature.histogram", "sensors",
		"Temperature sensor distribution", "sensors", 28620, sensorHistogramDims(), nil)
	if _, ok := reg.Chart("cpu.temperature"); !ok {
		w.winEnsure(reg, "cpu.temperature", "cpu.temperature", "cpu", "Core temperature", "Celsius",
			28600, cpuDims, nil)
	} else if ch, ok := reg.Chart("cpu.temperature"); ok {
		for _, d := range cpuDims {
			ch.AddDimension(d)
		}
	}
}

func (w *windowsCollector) collectSensors(reg *registry.Registry, now time.Time, s perflibSnap) {
	temps := perflibSensorReadings(s)
	if len(temps) == 0 {
		return
	}
	cpuVals := map[string]float64{}
	if ch, ok := reg.Chart("cpu.temperature"); ok {
		for _, t := range temps {
			if ch.Dimension(t.id) == nil {
				ch.AddDimension(&registry.Dimension{ID: t.id, Name: t.name})
			}
			cpuVals[t.id] = t.celsius
		}
		_ = reg.Collect("cpu.temperature", now, cpuVals)
	}
	for _, t := range temps {
		_ = reg.Collect("system.hw.sensor.temperature.input."+t.id, now, map[string]float64{"input": t.celsius})
	}
	_ = reg.Collect("system.hw.sensor.temperature.histogram", now, sensorHistogramVals(temps))
}

func serviceStateDims() []*registry.Dimension {
	return []*registry.Dimension{
		{ID: "running"}, {ID: "stopped"}, {ID: "start_pending"}, {ID: "stop_pending"},
		{ID: "continue_pending"}, {ID: "pause_pending"}, {ID: "paused"}, {ID: "unknown"},
	}
}

func (w *windowsCollector) ensureServiceCharts(reg *registry.Registry, rows []winServiceRow) {
	w.winEnsure(reg, "windows.services", "windows.services", "services", "Windows services by state", "services",
		20090, []*registry.Dimension{{ID: "running"}, {ID: "stopped"}, {ID: "other"}}, nil)
	for _, r := range rows {
		id := "windows.service_state." + sanitizeID(r.Name)
		w.winEnsure(reg, id, "windows.service_state", "services", "Service "+r.Name+" state", "state",
			20091, serviceStateDims(), map[string]string{"service": r.Name})
	}
}

func (w *windowsCollector) collectServiceCharts(reg *registry.Registry, now time.Time, rows []winServiceRow) {
	running, stopped, other := 0.0, 0.0, 0.0
	for _, r := range rows {
		st := strings.ToLower(r.State)
		vals := map[string]float64{
			"running": 0, "stopped": 0, "start_pending": 0, "stop_pending": 0,
			"continue_pending": 0, "pause_pending": 0, "paused": 0, "unknown": 0,
		}
		switch st {
		case "running":
			vals["running"] = 1
			running++
		case "stopped":
			vals["stopped"] = 1
			stopped++
		case "start_pending", "stop_pending", "continue_pending", "pause_pending", "paused":
			vals[st] = 1
			other++
		default:
			vals["unknown"] = 1
			other++
		}
		_ = reg.Collect("windows.service_state."+sanitizeID(r.Name), now, vals)
	}
	_ = reg.Collect("windows.services", now, map[string]float64{"running": running, "stopped": stopped, "other": other})
}
