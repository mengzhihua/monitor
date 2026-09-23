package collect

import (
	"context"
	"os"
	"os/user"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/process"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// appsConfig is the `collectors.modules.apps` section. Groups map a group
// name to process-name patterns (path.Match globs, case-insensitive); the
// first matching group wins, unmatched processes land in "other".
type appsConfig struct {
	Groups   map[string][]string `yaml:"groups"`
	Defaults *bool               `yaml:"defaults"` // merge built-in groups (default true)
	Cmdline  bool                `yaml:"cmdline"`  // also match patterns against the full command line
	Top      int                 `yaml:"top"`      // rows returned by the processes function (default 200)
}

// defaultAppGroups mirrors the spirit of Netdata's apps_groups.conf.
var defaultAppGroups = []struct {
	name     string
	patterns []string
}{
	{"monitor", []string{"monitord", "*.plugin"}},
	{"netdata", []string{"netdata", "apps.plugin", "go.d.plugin", "ebpf.plugin"}},
	{"ssh", []string{"sshd", "ssh", "sshd-session"}},
	{"kernel", []string{"kthreadd", "kworker*", "ksoftirqd*", "migration*", "rcu_*", "kswapd*", "cpuhp*", "idle_inject*", "watchdog*"}},
	{"system", []string{"systemd*", "init", "launchd", "udevd", "dbus*", "polkitd", "cron*", "atd", "rsyslogd", "journald", "logind", "snapd", "svchost", "wininit", "services", "lsass", "csrss", "smss", "winlogon", "System"}},
	{"containers", []string{"docker*", "containerd*", "runc*", "podman*", "crio*", "kubelet", "kube-*"}},
	{"vms", []string{"qemu*", "kvm*", "VBox*", "vmware*", "libvirtd", "virtqemud"}},
	{"database", []string{"mysqld", "mariadbd", "postgres*", "mongod", "redis-server", "valkey-server", "memcached", "clickhouse*", "etcd", "influxd", "elasticsearch", "java*elasticsearch"}},
	{"httpd", []string{"nginx", "httpd", "apache2", "caddy", "traefik", "haproxy", "envoy", "lighttpd"}},
	{"mq", []string{"kafka*", "rabbitmq*", "beam.smp", "mosquitto", "nats-server", "pulsar*"}},
	{"dev", []string{"gopls", "go", "node", "npm", "python*", "java", "ruby", "php*", "code", "code-*", "Code Helper*", "idea*", "goland*", "webstorm*", "pycharm*", "cursor*"}},
	{"shell", []string{"bash", "zsh", "sh", "fish", "dash", "tmux*", "screen", "cmd", "powershell", "pwsh*"}},
	{"desktop", []string{"gnome-*", "kwin*", "plasma*", "Xorg", "Xwayland", "wayland*", "pipewire*", "pulseaudio", "WindowServer", "Finder", "Dock", "explorer", "dwm"}},
	{"browser", []string{"chrome*", "chromium*", "firefox*", "Google Chrome*", "Safari*", "com.apple.WebKit*", "msedge*", "brave*"}},
	{"security", []string{"fail2ban*", "auditd", "clamd", "freshclam", "MsMpEng", "falcon*"}},
	{"backup", []string{"rsync", "restic", "borg", "rclone", "bacula*"}},
	{"time", []string{"chronyd", "ntpd", "systemd-timesyncd", "timesyncd"}},
	{"logs", []string{"fluent*", "vector", "filebeat", "promtail", "logstash", "loki"}},
	{"vpn", []string{"openvpn*", "wg-*", "tailscaled", "wireguard*", "strongswan", "charon"}},
	{"dns", []string{"named", "unbound", "dnsmasq", "coredns", "systemd-resolve*", "resolved"}},
}

type appGroup struct {
	name     string
	patterns []string
}

type pidState struct {
	name      string
	startedAt int64 // process birth identity; zero if unavailable
	cmdline   string
	group     *appGroup
	user      string
	osGroup   string
	cpuMs     float64 // user+system, ms
	hasCPU    bool
	readB     uint64
	writeB    uint64
	ioOK      bool
	skipIO    bool // permission or missing io; don't reopen it every tick
	seenAt    time.Time
	rss       uint64
	threads   int32
	ppid      int32
	cpuPct    float64 // computed on the last tick
}

type appsCollector struct {
	cfg    appsConfig
	groups []*appGroup
	other  *appGroup
	hasIO  bool

	mu   sync.Mutex
	pids map[int32]*pidState
	// monotonically increasing per-group counters fed as Incremental
	cpuMs               map[string]float64
	readB               map[string]float64
	writeB              map[string]float64
	cpuUser, cpuOSGroup map[string]float64
	groupCache          map[string]string
	userCache           map[string]string
	pidBuf              []int32
	scratch             []byte
	last                time.Time
}

// procCounters is one process sample. ok is false when CPU counters could not be read.
type procCounters struct {
	name            string
	ppid            int32
	cpuSec          float64
	rss             uint64
	threads         int32
	readB, writeB   uint64
	hasIO, ioDenied bool
	kthread         bool
	ok              bool
}

func init() {
	Register("apps", func() Collector { return &appsCollector{} })
}

func (a *appsCollector) Name() string { return "apps" }

func (a *appsCollector) Configure(decode func(v any) error) error {
	if err := decode(&a.cfg); err != nil {
		return err
	}
	if a.cfg.Top <= 0 {
		a.cfg.Top = 200
	}
	a.other = &appGroup{name: "other"}
	// user groups first (so they override built-ins), then defaults
	names := make([]string, 0, len(a.cfg.Groups))
	for n := range a.cfg.Groups {
		names = append(names, n)
	}
	sort.Strings(names)
	seen := map[string]*appGroup{}
	for _, n := range names {
		g := &appGroup{name: n, patterns: stripExe(a.cfg.Groups[n])}
		a.groups = append(a.groups, g)
		seen[n] = g
	}
	if a.cfg.Defaults == nil || *a.cfg.Defaults {
		for _, d := range defaultAppGroups {
			if g, ok := seen[d.name]; ok {
				g.patterns = append(g.patterns, d.patterns...)
				continue
			}
			g := &appGroup{name: d.name, patterns: d.patterns}
			a.groups = append(a.groups, g)
			seen[d.name] = g
		}
	}
	return nil
}

// stripExe drops a trailing ".exe" from patterns: process names are matched
// without the Windows executable suffix (see Collect).
func stripExe(ps []string) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, strings.TrimSuffix(strings.TrimSuffix(p, ".exe"), ".EXE"))
	}
	return out
}

func (a *appsCollector) Init(reg *registry.Registry) error {
	if a.other == nil { // Configure not called (no config section)
		if err := a.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := process.Pids(); err != nil {
		return err
	}
	a.pids = map[int32]*pidState{}
	a.cpuMs, a.readB, a.writeB = map[string]float64{}, map[string]float64{}, map[string]float64{}
	a.cpuUser, a.cpuOSGroup = map[string]float64{}, map[string]float64{}
	a.groupCache = map[string]string{}
	a.userCache = map[string]string{}

	all := append(append([]*appGroup{}, a.groups...), a.other)
	mk := func(id, title, units string, prio int, algo registry.Algorithm, mul, div int64) *registry.Chart {
		ch := &registry.Chart{ID: id, Family: "apps", Title: title, Units: units, Type: registry.Stacked,
			Priority: prio, Plugin: "apps", Module: "apps"}
		for _, g := range all {
			ch.Dimensions = append(ch.Dimensions, &registry.Dimension{ID: g.name, Algorithm: algo, Multiplier: mul, Divisor: div})
		}
		return ch
	}
	reg.AddChart(mk("apps.cpu", "Apps CPU utilization", "percentage", 20000, registry.Incremental, 1, 10)) // ms/s → %
	reg.AddChart(mk("apps.mem", "Apps resident memory", "MiB", 20010, registry.Absolute, 1, 1<<20))
	reg.AddChart(mk("apps.processes", "Apps processes", "processes", 20020, registry.Absolute, 1, 1))
	reg.AddChart(mk("apps.threads", "Apps threads", "threads", 20030, registry.Absolute, 1, 1))
	if a.hasIO = probeProcIO(); a.hasIO {
		reg.AddChart(mk("apps.io_read", "Apps disk read", "KiB/s", 20040, registry.Incremental, 1, 1024))
		reg.AddChart(mk("apps.io_write", "Apps disk write", "KiB/s", 20041, registry.Incremental, 1, 1024))
	}
	for _, ch := range []*registry.Chart{
		{ID: "apps.cpu_user", Family: "apps", Title: "Apps CPU by user", Units: "percentage", Type: registry.Stacked, Priority: 20050, Plugin: "apps", Module: "apps"},
		{ID: "apps.cpu_group", Family: "apps", Title: "Apps CPU by user group", Units: "percentage", Type: registry.Stacked, Priority: 20051, Plugin: "apps", Module: "apps"},
		{ID: "apps.mem_user", Family: "apps", Title: "Apps memory by user", Units: "MiB", Type: registry.Stacked, Priority: 20052, Plugin: "apps", Module: "apps"},
		{ID: "apps.mem_group", Family: "apps", Title: "Apps memory by user group", Units: "MiB", Type: registry.Stacked, Priority: 20053, Plugin: "apps", Module: "apps"},
		{ID: "apps.processes_user", Family: "apps", Title: "Apps processes by user", Units: "processes", Type: registry.Stacked, Priority: 20054, Plugin: "apps", Module: "apps"},
		{ID: "apps.processes_group", Family: "apps", Title: "Apps processes by user group", Units: "processes", Type: registry.Stacked, Priority: 20055, Plugin: "apps", Module: "apps"},
	} {
		reg.AddChart(ch)
	}
	return nil
}

func probeProcIO() bool {
	p, err := process.NewProcess(int32(os.Getpid()))
	if err != nil {
		return false
	}
	_, err = p.IOCounters()
	return err == nil
}

func (a *appsCollector) match(name, cmdline string) *appGroup {
	for _, g := range a.groups {
		for _, p := range g.patterns {
			if globMatch(p, name) {
				return g
			}
			if a.cfg.Cmdline && cmdline != "" && (globMatch(p, cmdline) || strings.Contains(strings.ToLower(cmdline), strings.ToLower(p))) {
				return g
			}
		}
	}
	return a.other
}

// appProcess carries identity from the same process-list snapshot as the PID.
// A fresh gopsutil handle reads live values without retaining cached metadata.
type appProcess struct {
	metadata  appProcessMetadata
	process   *process.Process
	startedAt int64
}

// Platforms without a process-table identity snapshot retain the gopsutil
// path. Command-line access is optional once a readable name is available.
func readPortableAppIdentity(ctx context.Context, p *process.Process) (name, cmdline string, err error) {
	if err = ctx.Err(); err != nil {
		return "", "", err
	}
	name, err = p.NameWithContext(ctx)
	if err != nil || name == "" {
		return name, "", err
	}
	cmdline, _ = p.CmdlineWithContext(ctx)
	return name, cmdline, ctx.Err()
}

func (a *appsCollector) readPortableOwners(ctx context.Context, p *process.Process, st *pidState) {
	if ctx.Err() != nil {
		return
	}
	st.ppid, _ = p.PpidWithContext(ctx)
	if uname, err := p.UsernameWithContext(ctx); err == nil {
		st.user = appsOwnerID(uname)
	}
	if gids, err := p.GidsWithContext(ctx); err == nil && len(gids) > 0 {
		st.osGroup = a.lookupOSGroup(gids[0])
	}
}

func (a *appsCollector) previousPID(pid int32, startedAt int64) *pidState {
	previous := a.pids[pid]
	if previous != nil && startedAt != 0 && previous.startedAt != startedAt {
		// The PID now belongs to a different process: reload identity and reset
		// CPU/IO baselines so its counters cannot be attributed to the old owner.
		return nil
	}
	return previous
}

func (a *appsCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	pids, native := listProcPIDs(a.pidBuf)
	a.pidBuf = pids
	var procs []appProcess
	if !native {
		var err error
		procs, err = listAppProcesses(ctx)
		if err != nil {
			return err
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	elapsed := now.Sub(a.last).Seconds()
	if a.last.IsZero() || elapsed <= 0 {
		elapsed = float64(reg.Host.UpdateEvery)
	}
	a.last = now

	mem := map[string]float64{}
	nproc := map[string]float64{}
	nthr := map[string]float64{}
	n := len(procs)
	if native {
		n = len(pids)
	}
	for i := 0; i < n; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var pid int32
		var startedAt int64
		var gp *process.Process
		if native {
			pid = pids[i]
		} else {
			gp = procs[i].process
			startedAt = procs[i].startedAt
			pid = gp.Pid
		}
		st := a.previousPID(pid, startedAt)
		var sample procCounters
		if native {
			sample = readProcSample(pid, st != nil && st.skipIO, &a.scratch)
		}
		if st == nil {
			if native {
				if !sample.ok || sample.name == "" {
					continue
				}
				st = &pidState{name: sample.name, ppid: sample.ppid}
				if !sample.kthread {
					st.cmdline = readProcCmdline(pid)
					if uid, gid, haveUID, haveGID := readProcOwners(pid); haveUID || haveGID {
						if haveUID {
							st.user = a.lookupUser(uid)
						}
						if haveGID {
							st.osGroup = a.lookupOSGroup(gid)
						}
					}
				}
				st.group = a.match(st.name, st.cmdline)
			} else {
				name, cmdline, err := procs[i].readIdentity(ctx)
				if err != nil || name == "" {
					continue // gone or not readable
				}
				if runtime.GOOS == "windows" {
					name = strings.TrimSuffix(name, ".exe") // so "chrome" matches chrome.exe
				}
				st = &pidState{name: name, cmdline: cmdline, startedAt: startedAt}
				st.group = a.match(name, st.cmdline)
				procs[i].readOwners(ctx, a, st)
			}
			a.pids[pid] = st
		}
		if native && !sample.ok {
			continue // exited between readdir and stat; drop it below
		}
		st.seenAt = now
		g := st.group.name
		var cpuSec float64
		var rss uint64
		var threads int32
		var readB, writeB uint64
		var hasIO, ok bool
		if native {
			if sample.ioDenied {
				st.skipIO = true
			}
			cpuSec, rss, threads = sample.cpuSec, sample.rss, sample.threads
			readB, writeB, hasIO, ok = sample.readB, sample.writeB, sample.hasIO, sample.ok
		} else {
			sample = readAppProcessSample(ctx, gp, a.hasIO)
			cpuSec, rss, threads = sample.cpuSec, sample.rss, sample.threads
			readB, writeB, hasIO, ok = sample.readB, sample.writeB, sample.hasIO, sample.ok
		}
		// A failed read retains its cumulative baseline, but cannot reuse the
		// previous interval's rate when aggregating CPU by user and OS group.
		st.cpuPct = 0
		if ok {
			ms := cpuSec * 1000
			if st.hasCPU {
				if d := ms - st.cpuMs; d > 0 {
					a.cpuMs[g] += d
					st.cpuPct = d / elapsed / 10
				}
			}
			st.cpuMs, st.hasCPU = ms, true
		}
		st.rss = rss
		mem[g] += float64(rss)
		st.threads = threads
		nthr[g] += float64(threads)
		nproc[g]++
		if a.hasIO && hasIO {
			if st.ioOK {
				if d := float64(readB) - float64(st.readB); d > 0 {
					a.readB[g] += d
				}
				if d := float64(writeB) - float64(st.writeB); d > 0 {
					a.writeB[g] += d
				}
			}
			st.readB, st.writeB, st.ioOK = readB, writeB, true
		}
	}
	for pid, st := range a.pids {
		if st.seenAt != now {
			delete(a.pids, pid)
		}
	}
	all := append(append([]*appGroup{}, a.groups...), a.other)
	cpu := make(map[string]float64, len(all))
	for _, g := range all {
		cpu[g.name] = a.cpuMs[g.name]
		if _, ok := mem[g.name]; !ok {
			mem[g.name], nproc[g.name], nthr[g.name] = 0, 0, 0
		}
	}
	_ = reg.Collect("apps.cpu", now, cpu)
	_ = reg.Collect("apps.mem", now, mem)
	_ = reg.Collect("apps.processes", now, nproc)
	_ = reg.Collect("apps.threads", now, nthr)
	if a.hasIO {
		rd, wr := make(map[string]float64, len(all)), make(map[string]float64, len(all))
		for _, g := range all {
			rd[g.name], wr[g.name] = a.readB[g.name], a.writeB[g.name]
		}
		_ = reg.Collect("apps.io_read", now, rd)
		_ = reg.Collect("apps.io_write", now, wr)
	}
	a.collectOwners(reg, now, elapsed)
	return nil
}

func (a *appsCollector) collectOwners(reg *registry.Registry, now time.Time, elapsed float64) {
	memU, nprocU := map[string]float64{}, map[string]float64{}
	memG, nprocG := map[string]float64{}, map[string]float64{}
	for _, st := range a.pids {
		if st.seenAt != now {
			continue
		}
		if st.user != "" {
			nprocU[st.user]++
			memU[st.user] += float64(st.rss)
			if st.hasCPU {
				// cpu delta already applied to group in Collect; recompute from cpuPct
				if st.cpuPct > 0 {
					a.cpuUser[st.user] += st.cpuPct * elapsed * 10
				}
			}
		}
		if st.osGroup != "" {
			nprocG[st.osGroup]++
			memG[st.osGroup] += float64(st.rss)
			if st.cpuPct > 0 {
				a.cpuOSGroup[st.osGroup] += st.cpuPct * elapsed * 10
			}
		}
	}
	addCPU := func(chart string, vals map[string]float64) {
		ch, ok := reg.Chart(chart)
		if !ok {
			return
		}
		for id := range vals {
			ch.AddDimension(&registry.Dimension{ID: id, Algorithm: registry.Incremental, Multiplier: 1, Divisor: 10})
		}
		_ = reg.Collect(chart, now, vals)
	}
	addAbs := func(chart string, vals map[string]float64, div int64) {
		ch, ok := reg.Chart(chart)
		if !ok {
			return
		}
		for id := range vals {
			ch.AddDimension(&registry.Dimension{ID: id, Algorithm: registry.Absolute, Multiplier: 1, Divisor: div})
		}
		_ = reg.Collect(chart, now, vals)
	}
	addCPU("apps.cpu_user", a.cpuUser)
	addCPU("apps.cpu_group", a.cpuOSGroup)
	addAbs("apps.mem_user", memU, 1<<20)
	addAbs("apps.mem_group", memG, 1<<20)
	addAbs("apps.processes_user", nprocU, 1)
	addAbs("apps.processes_group", nprocG, 1)
}

func (a *appsCollector) lookupUser(uid uint32) string {
	key := strconv.FormatUint(uint64(uid), 10)
	return a.lookupUserID(key, key)
}

// Linux retains its cached numeric fallback; Darwin leaves unavailable account
// names empty and retries for later PIDs if the directory service recovers.
func (a *appsCollector) lookupUserID(key, fallback string) string {
	if a.userCache != nil {
		if n, ok := a.userCache[key]; ok {
			return n
		}
	}
	name := fallback
	if u, err := user.LookupId(key); err == nil && u.Username != "" {
		name = u.Username
	} else if fallback == "" {
		return ""
	}
	name = appsOwnerID(name)
	if a.userCache != nil {
		a.userCache[key] = name
	}
	return name
}

func (a *appsCollector) lookupOSGroup(gid uint32) string {
	key := strconv.FormatUint(uint64(gid), 10)
	if a.groupCache != nil {
		if n, ok := a.groupCache[key]; ok {
			return n
		}
	}
	name := key
	if g, err := user.LookupGroupId(key); err == nil && g.Name != "" {
		name = g.Name
	}
	name = appsOwnerID(name)
	if a.groupCache != nil {
		a.groupCache[key] = name
	}
	return name
}

func appsOwnerID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.LastIndexAny(s, `/\`); i >= 0 {
		s = s[i+1:]
	}
	return sanitizeID(s)
}

// ProcessRow is one line of the `processes` function output.
type ProcessRow struct {
	PID     int32   `json:"pid"`
	PPID    int32   `json:"ppid"`
	Name    string  `json:"name"`
	Group   string  `json:"group"`
	CPU     float64 `json:"cpu"`     // percent of one core
	RSS     uint64  `json:"rss"`     // bytes
	Threads int32   `json:"threads"` //
	Cmdline string  `json:"cmdline"`
}

// Table is the generic function result shape (Netdata-style columns/rows).
type Table struct {
	Columns []string `json:"columns"`
	Rows    []any    `json:"rows"`
	Total   int      `json:"total"`
}

func (a *appsCollector) Functions() []Function {
	return []Function{{
		Name:    "processes",
		Help:    "Live process table (top by CPU, then RSS)",
		Timeout: 10,
		Run: func(_ context.Context, args map[string]string) (any, error) {
			return a.processes(args), nil
		},
	}}
}

func (a *appsCollector) processes(args map[string]string) Table {
	a.mu.Lock()
	rows := make([]ProcessRow, 0, len(a.pids))
	for pid, st := range a.pids {
		rows = append(rows, ProcessRow{PID: pid, PPID: st.ppid, Name: st.name, Group: st.group.name,
			CPU: st.cpuPct, RSS: st.rss, Threads: st.threads, Cmdline: st.cmdline})
	}
	a.mu.Unlock()
	if g := args["group"]; g != "" {
		f := rows[:0]
		for _, r := range rows {
			if r.Group == g {
				f = append(f, r)
			}
		}
		rows = f
	}
	sortBy := args["sort"]
	sort.Slice(rows, func(i, j int) bool {
		switch sortBy {
		case "rss", "mem":
			if rows[i].RSS != rows[j].RSS {
				return rows[i].RSS > rows[j].RSS
			}
		case "pid":
			return rows[i].PID < rows[j].PID
		default:
			if rows[i].CPU != rows[j].CPU {
				return rows[i].CPU > rows[j].CPU
			}
			if rows[i].RSS != rows[j].RSS {
				return rows[i].RSS > rows[j].RSS
			}
		}
		return rows[i].PID < rows[j].PID
	})
	total := len(rows)
	if len(rows) > a.cfg.Top {
		rows = rows[:a.cfg.Top]
	}
	out := Table{Columns: []string{"pid", "ppid", "name", "group", "cpu", "rss", "threads", "cmdline"}, Total: total}
	out.Rows = make([]any, len(rows))
	for i, r := range rows {
		out.Rows[i] = r
	}
	return out
}
