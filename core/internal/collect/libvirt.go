package collect

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// libvirtConfig is collectors.modules.libvirt (`virsh list` + `virsh domstats`).
type libvirtConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type libvirtCollector struct {
	cfg  libvirtConfig
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	seen map[string]bool
}

func init() {
	Register("libvirt", func() Collector { return &libvirtCollector{} })
}

func (l *libvirtCollector) Name() string { return "libvirt" }

func (l *libvirtCollector) Configure(decode func(v any) error) error {
	if err := decode(&l.cfg); err != nil {
		return err
	}
	if l.cfg.Command == "" {
		l.cfg.Command = "virsh"
	}
	if l.cfg.Timeout <= 0 {
		l.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (l *libvirtCollector) Init(reg *registry.Registry) error {
	if l.cfg.Command == "" {
		if err := l.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if l.run == nil {
		l.run = execRun(l.cfg.Timeout)
	}
	if _, err := l.domains(context.Background()); err != nil {
		return err
	}
	l.seen = map[string]bool{}
	return nil
}

func (l *libvirtCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	doms, err := l.domains(ctx)
	if err != nil {
		return err
	}
	for _, d := range doms {
		st, err := l.stats(ctx, d.Name)
		if err != nil {
			st = libvirtStats{Name: d.Name, State: d.State}
		}
		id := sanitizeID(d.Name)
		l.ensure(reg, d.Name, id)
		running, shut := 0.0, 1.0
		if d.State == "running" || st.State == "running" || st.StateCode == 1 {
			running, shut = 1, 0
		}
		_ = reg.Collect("libvirt.vm_status."+id, now, map[string]float64{"running": running, "shutoff": shut})
		_ = reg.Collect("libvirt.vm_cpu."+id, now, map[string]float64{"time": st.CPUTime})
		_ = reg.Collect("libvirt.vm_mem."+id, now, map[string]float64{"current": st.BalloonCur, "maximum": st.BalloonMax})
		_ = reg.Collect("libvirt.vm_net."+id, now, map[string]float64{"received": st.RxBytes, "sent": st.TxBytes})
		_ = reg.Collect("libvirt.vm_disk."+id, now, map[string]float64{"read": st.RdBytes, "write": st.WrBytes})
	}
	return nil
}

func (l *libvirtCollector) ensure(reg *registry.Registry, name, id string) {
	if l.seen[id] {
		return
	}
	l.seen[id] = true
	lbl := map[string]string{"domain": name}
	add := func(c *registry.Chart) {
		c.Family, c.Plugin, c.Module, c.Labels = "libvirt", "libvirt", "libvirt", lbl
		reg.AddChart(c)
	}
	add(&registry.Chart{ID: "libvirt.vm_status." + id, Context: "libvirt.vm_status", Title: "libvirt " + name + " status",
		Units: "status", Priority: 58000, Dimensions: []*registry.Dimension{{ID: "running"}, {ID: "shutoff"}}})
	add(&registry.Chart{ID: "libvirt.vm_cpu." + id, Context: "libvirt.vm_cpu", Title: "libvirt " + name + " CPU time",
		Units: "ns/s", Priority: 58010, Dimensions: []*registry.Dimension{incDim("time")}})
	add(&registry.Chart{ID: "libvirt.vm_mem." + id, Context: "libvirt.vm_mem", Title: "libvirt " + name + " balloon",
		Units: "KiB", Priority: 58020, Type: registry.Stacked, Dimensions: []*registry.Dimension{{ID: "current"}, {ID: "maximum", Hidden: true}}})
	add(&registry.Chart{ID: "libvirt.vm_net." + id, Context: "libvirt.vm_net", Title: "libvirt " + name + " network",
		Units: "bytes/s", Priority: 58030, Type: registry.Area,
		Dimensions: []*registry.Dimension{incDim("received"), {ID: "sent", Algorithm: registry.Incremental, Multiplier: -1}}})
	add(&registry.Chart{ID: "libvirt.vm_disk." + id, Context: "libvirt.vm_disk", Title: "libvirt " + name + " disk",
		Units: "bytes/s", Priority: 58040, Type: registry.Area,
		Dimensions: []*registry.Dimension{incDim("read"), {ID: "write", Algorithm: registry.Incremental, Multiplier: -1}}})
}

type libvirtDomain struct{ Name, State string }

type libvirtStats struct {
	Name, State                        string
	StateCode                          float64
	CPUTime, BalloonCur, BalloonMax    float64
	RxBytes, TxBytes, RdBytes, WrBytes float64
}

func (l *libvirtCollector) domains(ctx context.Context) ([]libvirtDomain, error) {
	out, err := l.run(ctx, l.cfg.Command, "list", "--all")
	if err != nil {
		return nil, err
	}
	doms := parseVirshList(string(out))
	if len(doms) == 0 {
		return nil, fmt.Errorf("libvirt: no domains")
	}
	return doms, nil
}

func (l *libvirtCollector) stats(ctx context.Context, name string) (libvirtStats, error) {
	out, err := l.run(ctx, l.cfg.Command, "domstats", "--cpu-total", "--balloon", "--block", "--interface", name)
	if err != nil {
		return libvirtStats{}, err
	}
	return parseVirshDomstats(string(out)), nil
}

func parseVirshList(s string) []libvirtDomain {
	var out []libvirtDomain
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "Id") || strings.Trim(line, "- ") == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		// Id Name State...  (Id may be "-")
		name, state := f[1], strings.Join(f[2:], " ")
		state = strings.ReplaceAll(state, " ", "")
		if state == "shutoff" || state == "shut-off" {
			state = "shutoff"
		}
		out = append(out, libvirtDomain{Name: name, State: state})
	}
	return out
}

func parseVirshDomstats(s string) libvirtStats {
	st := libvirtStats{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "Domain:") {
			st.Name = strings.Trim(strings.TrimPrefix(line, "Domain:"), " '")
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		n, _ := strconvFloat(v)
		switch {
		case k == "state.state":
			st.StateCode = n
			if n == 1 {
				st.State = "running"
			}
		case k == "cpu.time":
			st.CPUTime = n
		case k == "balloon.current":
			st.BalloonCur = n
		case k == "balloon.maximum":
			st.BalloonMax = n
		case strings.HasSuffix(k, ".rx.bytes"):
			st.RxBytes += n
		case strings.HasSuffix(k, ".tx.bytes"):
			st.TxBytes += n
		case strings.HasSuffix(k, ".rd.bytes"):
			st.RdBytes += n
		case strings.HasSuffix(k, ".wr.bytes"):
			st.WrBytes += n
		}
	}
	return st
}

func strconvFloat(s string) (float64, error) {
	return parseFloat(s)
}

func parseFloat(s string) (float64, error) {
	var n float64
	_, err := fmt.Sscan(s, &n)
	return n, err
}
