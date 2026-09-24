package collect

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// lvmConfig is collectors.modules.lvm (Netdata go.d lvm).
type lvmConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type lvmCollector struct {
	cfg    lvmConfig
	run    func(ctx context.Context, name string, args ...string) ([]byte, error)
	lvSeen map[string]bool
	last   time.Time
}

const lvmEvery = 15 * time.Second

func init() {
	Register("lvm", func() Collector { return &lvmCollector{} })
}

func (l *lvmCollector) Name() string { return "lvm" }

func (l *lvmCollector) Configure(decode func(v any) error) error {
	if err := decode(&l.cfg); err != nil {
		return err
	}
	if l.cfg.Command == "" {
		l.cfg.Command = "lvs"
	}
	if l.cfg.Timeout <= 0 {
		l.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (l *lvmCollector) Init(reg *registry.Registry) error {
	if l.cfg.Command == "" {
		if err := l.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	l.lvSeen = map[string]bool{}
	rows, err := l.list(context.Background())
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("lvm: no logical volumes")
	}
	return nil
}

func (l *lvmCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	if !sampleDue(&l.last, now, lvmEvery) {
		return nil
	}
	rows, err := l.list(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("lvm: no logical volumes")
	}
	for _, lv := range rows {
		l.ensure(reg, lv)
		id := sanitizeID(lv.VG + "_" + lv.Name)
		_ = reg.Collect("lvm.lv_data_percent."+id, now, map[string]float64{"data": lv.Data})
		if _, ok := reg.Chart("lvm.lv_metadata_percent." + id); ok {
			_ = reg.Collect("lvm.lv_metadata_percent."+id, now, map[string]float64{"metadata": lv.Meta})
		}
	}
	return nil
}

type lvmLV struct {
	Name, VG   string
	Size       float64
	Data, Meta float64
}

func (l *lvmCollector) list(ctx context.Context) ([]lvmLV, error) {
	run := l.run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cctx, cancel := context.WithTimeout(ctx, l.cfg.Timeout)
			defer cancel()
			cmd := exec.CommandContext(cctx, name, args...)
			cmd.WaitDelay = execWaitDelay
			return cmd.Output()
		}
	}
	out, err := run(ctx, l.cfg.Command, "--noheadings", "--units", "b", "--nosuffix",
		"-o", "lv_name,vg_name,lv_size,data_percent,metadata_percent")
	if err != nil {
		return nil, fmt.Errorf("lvs: %w", err)
	}
	rows := parseLVS(string(out))
	if len(rows) == 0 {
		return nil, fmt.Errorf("lvm: no logical volumes")
	}
	return rows, nil
}

func (l *lvmCollector) ensure(reg *registry.Registry, lv lvmLV) {
	id := sanitizeID(lv.VG + "_" + lv.Name)
	if l.lvSeen[id] {
		return
	}
	l.lvSeen[id] = true
	lbl := map[string]string{"lv": lv.Name, "vg": lv.VG}
	title := lv.VG + "/" + lv.Name
	reg.AddChart(&registry.Chart{ID: "lvm.lv_data_percent." + id, Context: "lvm.lv_data_percent",
		Family: "lvm", Title: "LVM data " + title, Units: "percentage", Priority: 49300,
		Plugin: "lvm", Module: "lvm", Labels: lbl, Dimensions: []*registry.Dimension{{ID: "data"}}})
	if lv.Meta > 0 {
		reg.AddChart(&registry.Chart{ID: "lvm.lv_metadata_percent." + id, Context: "lvm.lv_metadata_percent",
			Family: "lvm", Title: "LVM metadata " + title, Units: "percentage", Priority: 49310,
			Plugin: "lvm", Module: "lvm", Labels: lbl, Dimensions: []*registry.Dimension{{ID: "metadata"}}})
	}
}

func parseLVS(s string) []lvmLV {
	var out []lvmLV
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "WARNING") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		lv := lvmLV{Name: f[0], VG: f[1], Size: firstFloat(f[2])}
		if len(f) > 3 {
			lv.Data = firstFloat(f[3])
		}
		if len(f) > 4 {
			lv.Meta = firstFloat(f[4])
		}
		out = append(out, lv)
	}
	return out
}
