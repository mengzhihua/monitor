package collect

import (
	"context"
	"os"
	"sort"
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
	{"system", []string{"systemd*", "init", "launchd", "udevd", "dbus*", "polkitd", "cron*", "atd", "rsyslogd", "journald", "logind", "snapd", "svchost.exe", "wininit.exe", "services.exe", "lsass.exe", "csrss.exe", "smss.exe", "winlogon.exe", "System"}},
	{"containers", []string{"docker*", "containerd*", "runc*", "podman*", "crio*", "kubelet", "kube-*"}},
	{"vms", []string{"qemu*", "kvm*", "VBox*", "vmware*", "libvirtd", "virtqemud"}},
	{"database", []string{"mysqld", "mariadbd", "postgres*", "mongod", "redis-server", "valkey-server", "memcached", "clickhouse*", "etcd", "influxd", "elasticsearch", "java*elasticsearch"}},
	{"httpd", []string{"nginx", "httpd", "apache2", "caddy", "traefik", "haproxy", "envoy", "lighttpd"}},
	{"mq", []string{"kafka*", "rabbitmq*", "beam.smp", "mosquitto", "nats-server", "pulsar*"}},
	{"dev", []string{"gopls", "go", "node", "npm", "python*", "java", "ruby", "php*", "code", "code-*", "Code Helper*", "idea*", "goland*", "webstorm*", "pycharm*", "cursor*"}},
	{"shell", []string{"bash", "zsh", "sh", "fish", "dash", "tmux*", "screen", "cmd.exe", "powershell.exe", "pwsh*"}},
	{"desktop", []string{"gnome-*", "kwin*", "plasma*", "Xorg", "Xwayland", "wayland*", "pipewire*", "pulseaudio", "WindowServer", "Finder", "Dock", "explorer.exe", "dwm.exe"}},
	{"browser", []string{"chrome*", "chromium*", "firefox*", "Google Chrome*", "Safari*", "com.apple.WebKit*", "msedge*", "brave*"}},
	{"security", []string{"fail2ban*", "auditd", "clamd", "freshclam", "MsMpEng.exe", "falcon*"}},
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
	name    string
	cmdline string
	group   *appGroup
	cpuMs   float64 // user+system, ms
	hasCPU  bool
	readB   uint64
	writeB  uint64
	ioOK    bool
	seenAt  time.Time
	rss     uint64
	threads int32
	ppid    int32
	cpuPct  float64 // computed on the last tick
}

type appsCollector struct {
	cfg    appsConfig
	groups []*appGroup
	other  *appGroup
	hasIO  bool

	mu   sync.Mutex
	pids map[int32]*pidState
	// monotonically increasing per-group counters fed as Incremental
	cpuMs  map[string]float64
	readB  map[string]float64
	writeB map[string]float64
	last   time.Time
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
		g := &appGroup{name: n, patterns: a.cfg.Groups[n]}
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

func (a *appsCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return err
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
	for _, p := range procs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		st := a.pids[p.Pid]
		if st == nil {
			name, err := p.NameWithContext(ctx)
			if err != nil || name == "" {
				continue // gone or not readable
			}
			st = &pidState{name: name}
			st.cmdline, _ = p.CmdlineWithContext(ctx)
			st.ppid, _ = p.PpidWithContext(ctx)
			st.group = a.match(name, st.cmdline)
			a.pids[p.Pid] = st
		}
		st.seenAt = now
		g := st.group.name
		if t, err := p.TimesWithContext(ctx); err == nil {
			ms := (t.User + t.System) * 1000
			st.cpuPct = 0
			if st.hasCPU {
				if d := ms - st.cpuMs; d > 0 {
					a.cpuMs[g] += d
					st.cpuPct = d / elapsed / 10
				}
			}
			st.cpuMs, st.hasCPU = ms, true
		}
		if m, err := p.MemoryInfoWithContext(ctx); err == nil && m != nil {
			st.rss = m.RSS
			mem[g] += float64(m.RSS)
		}
		if n, err := p.NumThreadsWithContext(ctx); err == nil {
			st.threads = n
			nthr[g] += float64(n)
		}
		nproc[g]++
		if a.hasIO {
			if io, err := p.IOCountersWithContext(ctx); err == nil && io != nil {
				if st.ioOK {
					if d := float64(io.ReadBytes) - float64(st.readB); d > 0 {
						a.readB[g] += d
					}
					if d := float64(io.WriteBytes) - float64(st.writeB); d > 0 {
						a.writeB[g] += d
					}
				}
				st.readB, st.writeB, st.ioOK = io.ReadBytes, io.WriteBytes, true
			}
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
	return nil
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
