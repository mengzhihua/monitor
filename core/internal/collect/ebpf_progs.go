package collect

import (
	"bufio"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// Portable approximation of Netdata ebpf.plugin program families. Prefer
// /sys/kernel/debug/tracing/kprobe_profile hits when probes are attached;
// otherwise fall back to /proc/vmstat, dentry-state, file-nr, etc.

func (e *ebpfCollector) probeProgSources() {
	e.vmstat = firstNonEmpty(e.vmstat, "/proc/vmstat")
	e.dentry = firstNonEmpty(e.dentry, "/proc/sys/fs/dentry-state")
	e.filenr = firstNonEmpty(e.filenr, "/proc/sys/fs/file-nr")
	e.inodes = firstNonEmpty(e.inodes, "/proc/sys/fs/inode-state")
	e.diskstats = firstNonEmpty(e.diskstats, "/proc/diskstats")
	e.mounts = firstNonEmpty(e.mounts, "/proc/self/mounts")
	e.kprobes = firstNonEmpty(e.kprobes, "/sys/kernel/debug/tracing/kprobe_profile")
	e.shm = firstNonEmpty(e.shm, "/proc/sysvipc/shm")
	e.stat = firstNonEmpty(e.stat, "/proc/stat")
	e.interrupts = firstNonEmpty(e.interrupts, "/proc/interrupts")

	if kp, err := readTrim(e.kprobes); err == nil && strings.TrimSpace(kp) != "" {
		e.haveKprobe = true
		fam := kprobeFamilies(parseKprobeProfile(kp))
		e.haveCache = e.haveCache || fam["ebpf.cachestat"]
		e.haveDC = e.haveDC || fam["ebpf.dcstat"]
		e.haveVFS = e.haveVFS || fam["ebpf.vfs"]
		e.haveOOM = e.haveOOM || fam["ebpf.oomkill"]
		e.haveProc = e.haveProc || fam["ebpf.process"]
		e.haveSHM = e.haveSHM || fam["ebpf.shm"]
		e.haveSwap = e.haveSwap || fam["ebpf.swap"]
		e.haveSync = e.haveSync || fam["ebpf.sync"]
		e.haveMD = e.haveMD || fam["ebpf.mdflush"]
		e.haveMount = e.haveMount || fam["ebpf.mount"]
		e.haveIRQ = e.haveIRQ || fam["ebpf.hardirq"]
		e.haveDisk = e.haveDisk || fam["ebpf.disk"]
		e.haveFD = e.haveFD || fam["ebpf.fd"]
	}
	if raw, err := readTrim(e.vmstat); err == nil {
		vm := parseVmstatMap(raw)
		if _, ok := vm["pgpgin"]; ok || vm["pgfault"] > 0 {
			e.haveCache = true
		}
		if _, ok := vm["oom_kill"]; ok {
			e.haveOOM = true
		}
		if _, ok := vm["pswpin"]; ok || vm["pswpout"] > 0 {
			e.haveSwap = true
		}
	}
	if _, err := readTrim(e.dentry); err == nil {
		e.haveDC = true
	}
	if _, err := readTrim(e.filenr); err == nil {
		e.haveFD = true
	}
	if _, err := readTrim(e.inodes); err == nil {
		e.haveVFS = true
	}
	if st, err := parseProcStatFile(e.stat); err == nil && st.forks > 0 {
		e.haveProc = true
	}
	if raw, err := readTrim(e.shm); err == nil && strings.Contains(raw, "key") {
		e.haveSHM = true
	}
	if _, err := readTrim(e.diskstats); err == nil {
		e.haveDisk = true
	}
	if _, err := readTrim(e.mounts); err == nil {
		e.haveMount = true
	}
	if _, err := readTrim(e.interrupts); err == nil {
		e.haveIRQ = true
	}
}

func (e *ebpfCollector) anyProgs() bool {
	return e.haveCache || e.haveDC || e.haveFD || e.haveVFS || e.haveOOM || e.haveProc ||
		e.haveSHM || e.haveSwap || e.haveDisk || e.haveMount || e.haveIRQ || e.haveSync || e.haveMD
}

func (e *ebpfCollector) addProgCharts(reg *registry.Registry) {
	add := func(id, title, units string, prio int, dims ...*registry.Dimension) {
		ch := sysChart(id, "ebpf", title, units, prio, dims...)
		ch.Plugin, ch.Module = "ebpf", "ebpf"
		reg.AddChart(ch)
	}
	if e.haveCache {
		add("ebpf.cachestat", "Page cache (eBPF/procfs)", "pages/s", 58240,
			incDim("hits"), incDim("misses"), incDim("dirtied"))
	}
	if e.haveDC {
		add("ebpf.dcstat", "Directory cache", "entries", 58241,
			&registry.Dimension{ID: "existing"}, &registry.Dimension{ID: "unused"})
	}
	if e.haveFD {
		add("ebpf.fd", "File descriptors (eBPF/procfs)", "descriptors", 58242,
			&registry.Dimension{ID: "allocated"}, &registry.Dimension{ID: "unused"}, &registry.Dimension{ID: "max", Hidden: true})
	}
	if e.haveVFS {
		add("ebpf.vfs", "VFS activity", "calls/s", 58243,
			incDim("read"), incDim("write"), incDim("open"), incDim("fsync"), incDim("unlink"), incDim("create"),
			&registry.Dimension{ID: "inodes"}, &registry.Dimension{ID: "unused_inodes"})
	}
	if e.haveOOM {
		add("ebpf.oomkill", "OOM kills", "kills/s", 58244, incDim("kills"))
	}
	if e.haveProc {
		add("ebpf.process", "Process lifecycle", "events/s", 58245, incDim("process"))
	}
	if e.haveSHM {
		add("ebpf.shm", "SysV shared memory", "segments", 58246, &registry.Dimension{ID: "segments"})
	}
	if e.haveSwap {
		add("ebpf.swap", "Swap I/O", "pages/s", 58247, incDim("read"), incDim("write"))
	}
	if e.haveSync {
		add("ebpf.sync", "Sync syscalls", "calls/s", 58248, incDim("sync"))
	}
	if e.haveMD {
		add("ebpf.mdflush", "MD flush requests", "flushes/s", 58249, incDim("flush"))
	}
	if e.haveMount {
		add("ebpf.mount", "Mount points", "mounts", 58250, &registry.Dimension{ID: "mounts"})
	}
	if e.haveIRQ {
		add("ebpf.hardirq", "Hardware interrupts", "interrupts/s", 58251, incDim("interrupts"))
	}
	if e.haveDisk {
		add("ebpf.disk", "Block I/O", "ops/s", 58252, incDim("reads"), incDim("writes"))
	}
}

func (e *ebpfCollector) collectProgs(reg *registry.Registry, now time.Time) {
	kp := map[string]float64{}
	if e.haveKprobe {
		if raw, err := readTrim(e.kprobes); err == nil {
			kp = parseKprobeProfile(raw)
		}
	}
	if e.haveCache {
		hits, misses, dirtied := kp["mark_page_accessed"], kp["add_to_page_cache_lru"], kp["account_page_dirtied"]+kp["mark_buffer_dirty"]
		if hits == 0 && misses == 0 && dirtied == 0 {
			if raw, err := readTrim(e.vmstat); err == nil {
				vm := parseVmstatMap(raw)
				misses = vm["pgpgin"]
				hits = vm["pgfault"] - vm["pgmajfault"]
				if hits < 0 {
					hits = 0
				}
				dirtied = vm["pgpgout"]
			}
		}
		_ = reg.Collect("ebpf.cachestat", now, map[string]float64{"hits": hits, "misses": misses, "dirtied": dirtied})
	}
	if e.haveDC {
		existing, unused := 0.0, 0.0
		if v, ok := kp["d_lookup"]; ok && v > 0 {
			existing = v
		}
		if raw, err := readTrim(e.dentry); err == nil {
			a, b := parseTwoInts(raw)
			existing, unused = a, b
		}
		_ = reg.Collect("ebpf.dcstat", now, map[string]float64{"existing": existing, "unused": unused})
	}
	if e.haveFD {
		if a, u, m, err := parseFileNRFile(e.filenr); err == nil {
			_ = reg.Collect("ebpf.fd", now, map[string]float64{"allocated": a, "unused": u, "max": m})
		} else if v := kp["do_sys_open"] + kp["do_sys_openat2"]; v > 0 {
			_ = reg.Collect("ebpf.fd", now, map[string]float64{"allocated": v, "unused": 0, "max": 0})
		}
	}
	if e.haveVFS {
		vals := map[string]float64{
			"read": kp["vfs_read"], "write": kp["vfs_write"], "open": kp["vfs_open"],
			"fsync": kp["vfs_fsync"], "unlink": kp["vfs_unlink"], "create": kp["vfs_create"],
		}
		if raw, err := readTrim(e.inodes); err == nil {
			a, b := parseTwoInts(raw)
			vals["inodes"], vals["unused_inodes"] = a, b
		}
		_ = reg.Collect("ebpf.vfs", now, vals)
	}
	if e.haveOOM {
		kills := kp["oom_kill_process"] + kp["mark_victim"]
		if kills == 0 {
			if raw, err := readTrim(e.vmstat); err == nil {
				kills = parseVmstatMap(raw)["oom_kill"]
			}
		}
		_ = reg.Collect("ebpf.oomkill", now, map[string]float64{"kills": kills})
	}
	if e.haveProc {
		n := kp["_do_fork"] + kp["kernel_clone"] + kp["do_fork"]
		if st, err := parseProcStatFile(e.stat); err == nil {
			n = st.forks
		}
		_ = reg.Collect("ebpf.process", now, map[string]float64{"process": n})
	}
	if e.haveSHM {
		n := 0.0
		if raw, err := readTrim(e.shm); err == nil {
			n = float64(countNonHeaderLines(raw))
		}
		_ = reg.Collect("ebpf.shm", now, map[string]float64{"segments": n})
	}
	if e.haveSwap {
		rd, wr := kp["swap_readpage"], kp["swap_writepage"]
		if rd == 0 && wr == 0 {
			if raw, err := readTrim(e.vmstat); err == nil {
				vm := parseVmstatMap(raw)
				rd, wr = vm["pswpin"], vm["pswpout"]
			}
		}
		_ = reg.Collect("ebpf.swap", now, map[string]float64{"read": rd, "write": wr})
	}
	if e.haveSync {
		_ = reg.Collect("ebpf.sync", now, map[string]float64{"sync": kp["sys_sync"] + kp["do_sync"] + kp["vfs_fsync"]})
	}
	if e.haveMD {
		_ = reg.Collect("ebpf.mdflush", now, map[string]float64{"flush": kp["md_flush_request"]})
	}
	if e.haveMount {
		n := 0.0
		if raw, err := readTrim(e.mounts); err == nil {
			n = float64(countNonHeaderLines(raw))
		}
		_ = reg.Collect("ebpf.mount", now, map[string]float64{"mounts": n})
	}
	if e.haveIRQ {
		n := kp["handle_irq_event_percpu"]
		if raw, err := readTrim(e.interrupts); err == nil {
			n = sumInterrupts(raw)
		}
		_ = reg.Collect("ebpf.hardirq", now, map[string]float64{"interrupts": n})
	}
	if e.haveDisk {
		rd, wr := kp["blk_account_io_start"], 0.0
		if raw, err := readTrim(e.diskstats); err == nil {
			rd, wr = sumDiskOps(raw)
		}
		_ = reg.Collect("ebpf.disk", now, map[string]float64{"reads": rd, "writes": wr})
	}
}

func parseVmstatMap(s string) map[string]float64 {
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

func parseKprobeProfile(s string) map[string]float64 {
	out := map[string]float64{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		name := strings.TrimPrefix(f[0], "p:")
		if i := strings.LastIndexByte(name, '/'); i >= 0 {
			name = name[i+1:]
		}
		hits, _ := strconv.ParseFloat(f[1], 64)
		out[name] += hits
	}
	return out
}

func kprobeFamilies(kp map[string]float64) map[string]bool {
	out := map[string]bool{}
	for name := range kp {
		switch name {
		case "add_to_page_cache_lru", "mark_page_accessed", "account_page_dirtied", "mark_buffer_dirty":
			out["ebpf.cachestat"] = true
		case "d_lookup":
			out["ebpf.dcstat"] = true
		case "vfs_read", "vfs_write", "vfs_open", "vfs_fsync", "vfs_unlink", "vfs_create":
			out["ebpf.vfs"] = true
		case "oom_kill_process", "mark_victim":
			out["ebpf.oomkill"] = true
		case "_do_fork", "kernel_clone", "do_fork":
			out["ebpf.process"] = true
		case "shmget", "shmat", "shmdt", "shmctl":
			out["ebpf.shm"] = true
		case "swap_readpage", "swap_writepage":
			out["ebpf.swap"] = true
		case "sys_sync", "do_sync":
			out["ebpf.sync"] = true
		case "md_flush_request":
			out["ebpf.mdflush"] = true
		case "attach_recursive_mnt":
			out["ebpf.mount"] = true
		case "handle_irq_event_percpu":
			out["ebpf.hardirq"] = true
		case "blk_account_io_start":
			out["ebpf.disk"] = true
		case "do_sys_open", "do_sys_openat2":
			out["ebpf.fd"] = true
		}
	}
	return out
}

func parseTwoInts(s string) (float64, float64) {
	f := strings.Fields(s)
	var a, b float64
	if len(f) > 0 {
		a, _ = strconv.ParseFloat(f[0], 64)
	}
	if len(f) > 1 {
		b, _ = strconv.ParseFloat(f[1], 64)
	}
	return a, b
}

func countNonHeaderLines(s string) int {
	n := 0
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "key") || strings.HasPrefix(line, "#") {
			continue
		}
		n++
	}
	return n
}

func sumInterrupts(s string) float64 {
	var total float64
	sc := bufio.NewScanner(strings.NewReader(s))
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		f := strings.Fields(sc.Text())
		for i := 1; i < len(f); i++ {
			if _, err := strconv.Atoi(f[i]); err != nil {
				break
			}
			v, _ := strconv.ParseFloat(f[i], 64)
			total += v
		}
	}
	return total
}

func sumDiskOps(s string) (reads, writes float64) {
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 8 {
			continue
		}
		name := f[2]
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") {
			continue
		}
		rd, _ := strconv.ParseFloat(f[3], 64)
		wr, _ := strconv.ParseFloat(f[7], 64)
		reads += rd
		writes += wr
	}
	return reads, writes
}
