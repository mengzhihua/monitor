package collect

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// nvmeConfig is collectors.modules.nvme (Netdata go.d nvme).
type nvmeConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type nvmeCollector struct {
	cfg     nvmeConfig
	run     func(ctx context.Context, name string, args ...string) ([]byte, error)
	devSeen map[string]bool
	last    time.Time
}

const nvmeEvery = 15 * time.Second

func init() {
	Register("nvme", func() Collector { return &nvmeCollector{} })
}

func (n *nvmeCollector) Name() string { return "nvme" }

func (n *nvmeCollector) Configure(decode func(v any) error) error {
	if err := decode(&n.cfg); err != nil {
		return err
	}
	if n.cfg.Command == "" {
		n.cfg.Command = "nvme"
	}
	if n.cfg.Timeout <= 0 {
		n.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (n *nvmeCollector) Init(reg *registry.Registry) error {
	if n.cfg.Command == "" {
		if err := n.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	n.devSeen = map[string]bool{}
	devs, err := n.list(context.Background())
	if err != nil {
		return err
	}
	if len(devs) == 0 {
		return fmt.Errorf("nvme: no devices")
	}
	return nil
}

func (n *nvmeCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	if !sampleDue(&n.last, now, nvmeEvery) {
		return nil
	}
	devs, err := n.list(ctx)
	if err != nil {
		return err
	}
	ok := 0
	var last error
	for _, d := range devs {
		st, err := n.smart(ctx, d)
		if err != nil {
			last = err
			continue
		}
		n.ensure(reg, d)
		id := sanitizeID(filepath.Base(d))
		_ = reg.Collect("nvme.device_temperature."+id, now, map[string]float64{"temperature": st.temp})
		_ = reg.Collect("nvme.device_available_spare."+id, now, map[string]float64{"spare": st.spare})
		_ = reg.Collect("nvme.device_percentage_used."+id, now, map[string]float64{"used": st.used})
		_ = reg.Collect("nvme.device_data_units."+id, now, map[string]float64{"read": st.read, "written": st.written})
		_ = reg.Collect("nvme.device_media_errors."+id, now, map[string]float64{"media_errors": st.media})
		ok++
	}
	if ok == 0 {
		if last != nil {
			return last
		}
		return fmt.Errorf("nvme: no device data")
	}
	return nil
}

type nvmeSmart struct{ temp, spare, used, read, written, media float64 }

func (n *nvmeCollector) exec(ctx context.Context, args ...string) ([]byte, error) {
	run := n.run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cctx, cancel := context.WithTimeout(ctx, n.cfg.Timeout)
			defer cancel()
			return exec.CommandContext(cctx, name, args...).Output()
		}
	}
	return run(ctx, n.cfg.Command, args...)
}

func (n *nvmeCollector) list(ctx context.Context) ([]string, error) {
	out, err := n.exec(ctx, "list")
	if err != nil {
		return nil, fmt.Errorf("nvme list: %w", err)
	}
	devs := parseNVMeList(string(out))
	if len(devs) == 0 {
		return nil, fmt.Errorf("nvme: no devices")
	}
	return devs, nil
}

func (n *nvmeCollector) smart(ctx context.Context, dev string) (nvmeSmart, error) {
	out, err := n.exec(ctx, "smart-log", dev)
	if err != nil && len(out) == 0 {
		return nvmeSmart{}, err
	}
	return parseNVMeSmart(string(out))
}

func (n *nvmeCollector) ensure(reg *registry.Registry, dev string) {
	name := filepath.Base(dev)
	id := sanitizeID(name)
	if n.devSeen[id] {
		return
	}
	n.devSeen[id] = true
	lbl := map[string]string{"device": name}
	mk := func(suffix, title, units string, prio int, dims ...*registry.Dimension) {
		reg.AddChart(&registry.Chart{ID: "nvme." + suffix + "." + id, Context: "nvme." + suffix,
			Family: "nvme", Title: title + " " + name, Units: units, Priority: prio,
			Plugin: "nvme", Module: "nvme", Labels: lbl, Dimensions: dims})
	}
	mk("device_temperature", "NVMe temperature", "Celsius", 49100, &registry.Dimension{ID: "temperature"})
	mk("device_available_spare", "NVMe available spare", "percentage", 49110, &registry.Dimension{ID: "spare"})
	mk("device_percentage_used", "NVMe percentage used", "percentage", 49120, &registry.Dimension{ID: "used"})
	mk("device_data_units", "NVMe data units", "units/s", 49130, incDim("read"), incDim("written"))
	mk("device_media_errors", "NVMe media errors", "errors", 49140, &registry.Dimension{ID: "media_errors"})
}

func parseNVMeList(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "/dev/nvme") {
			f := strings.Fields(line)
			if !seen[f[0]] {
				seen[f[0]] = true
				out = append(out, f[0])
			}
			continue
		}
		// `nvme list` table: Node SN Model ...
		if strings.HasPrefix(line, "/dev/") {
			f := strings.Fields(line)
			if !seen[f[0]] {
				seen[f[0]] = true
				out = append(out, f[0])
			}
		}
	}
	return out
}

func parseNVMeSmart(s string) (nvmeSmart, error) {
	st := nvmeSmart{}
	found := false
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		val := strings.TrimSpace(v)
		num := firstFloat(val)
		switch {
		case strings.Contains(key, "temperature") && !strings.Contains(key, "sensor"):
			st.temp = num
			found = true
		case strings.Contains(key, "available_spare") && !strings.Contains(key, "threshold"):
			st.spare = num
			found = true
		case strings.Contains(key, "percentage_used"):
			st.used = num
			found = true
		case strings.Contains(key, "data_units_read"):
			st.read = num
		case strings.Contains(key, "data_units_written"):
			st.written = num
		case strings.Contains(key, "media_errors"):
			st.media = num
		}
	}
	if !found {
		return nvmeSmart{}, fmt.Errorf("nvme: cannot parse smart-log")
	}
	return st, nil
}
