package collect

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// chronyConfig is collectors.modules.chrony (Netdata go.d chrony).
type chronyConfig struct {
	Command string        `yaml:"command"` // default chronyc
	Timeout time.Duration `yaml:"timeout"`
}

type chronyCollector struct {
	cfg  chronyConfig
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	last time.Time
}

const chronyEvery = 10 * time.Second

func init() {
	Register("chrony", func() Collector { return &chronyCollector{} })
}

func (c *chronyCollector) Name() string { return "chrony" }

func (c *chronyCollector) Configure(decode func(v any) error) error {
	if err := decode(&c.cfg); err != nil {
		return err
	}
	if c.cfg.Command == "" {
		c.cfg.Command = "chronyc"
	}
	if c.cfg.Timeout <= 0 {
		c.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (c *chronyCollector) Init(reg *registry.Registry) error {
	if c.cfg.Command == "" {
		if err := c.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, _, err := c.sample(context.Background()); err != nil {
		return err
	}
	for _, ch := range []*registry.Chart{
		{ID: "chrony.stratum", Title: "Chrony stratum", Units: "level", Priority: 48000, Dimensions: []*registry.Dimension{{ID: "stratum"}}},
		{ID: "chrony.current_correction", Title: "Chrony current correction", Units: "seconds", Priority: 48010, Dimensions: []*registry.Dimension{{ID: "current_correction"}}},
		{ID: "chrony.last_offset", Title: "Chrony last offset", Units: "seconds", Priority: 48020, Dimensions: []*registry.Dimension{{ID: "offset"}}},
		{ID: "chrony.rms_offset", Title: "Chrony RMS offset", Units: "seconds", Priority: 48021, Dimensions: []*registry.Dimension{{ID: "offset"}}},
		{ID: "chrony.root_delay", Title: "Chrony root delay", Units: "seconds", Priority: 48030, Dimensions: []*registry.Dimension{{ID: "root_delay"}}},
		{ID: "chrony.root_dispersion", Title: "Chrony root dispersion", Units: "seconds", Priority: 48031, Dimensions: []*registry.Dimension{{ID: "root_dispersion"}}},
		{ID: "chrony.frequency", Title: "Chrony frequency", Units: "ppm", Priority: 48040, Dimensions: []*registry.Dimension{{ID: "frequency"}}},
		{ID: "chrony.residual_frequency", Title: "Chrony residual frequency", Units: "ppm", Priority: 48041, Dimensions: []*registry.Dimension{{ID: "residual_frequency"}}},
		{ID: "chrony.skew", Title: "Chrony skew", Units: "ppm", Priority: 48042, Dimensions: []*registry.Dimension{{ID: "skew"}}},
		{ID: "chrony.update_interval", Title: "Chrony update interval", Units: "seconds", Priority: 48050, Dimensions: []*registry.Dimension{{ID: "update_interval"}}},
		{ID: "chrony.leap_status", Title: "Chrony leap status", Units: "status", Type: registry.Stacked, Priority: 48060,
			Dimensions: []*registry.Dimension{{ID: "normal"}, {ID: "insert_second"}, {ID: "delete_second"}, {ID: "unsynchronised"}}},
		{ID: "chrony.activity", Title: "Chrony peers activity", Units: "sources", Type: registry.Stacked, Priority: 48070,
			Dimensions: []*registry.Dimension{{ID: "online"}, {ID: "offline"}, {ID: "burst_online"}, {ID: "burst_offline"}, {ID: "unresolved"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "chrony", "chrony", "chrony"
		reg.AddChart(ch)
	}
	return nil
}

type chronySample struct {
	stratum, correction, lastOff, rmsOff, rootDelay, rootDisp float64
	freq, residual, skew, update                              float64
	leap                                                      string
	online, offline, burstOn, burstOff, unresolved            float64
}

func (c *chronyCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	if !sampleDue(&c.last, now, chronyEvery) {
		return nil
	}
	s, err := c.stats(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("chrony.stratum", now, map[string]float64{"stratum": s.stratum})
	_ = reg.Collect("chrony.current_correction", now, map[string]float64{"current_correction": s.correction})
	_ = reg.Collect("chrony.last_offset", now, map[string]float64{"offset": s.lastOff})
	_ = reg.Collect("chrony.rms_offset", now, map[string]float64{"offset": s.rmsOff})
	_ = reg.Collect("chrony.root_delay", now, map[string]float64{"root_delay": s.rootDelay})
	_ = reg.Collect("chrony.root_dispersion", now, map[string]float64{"root_dispersion": s.rootDisp})
	_ = reg.Collect("chrony.frequency", now, map[string]float64{"frequency": s.freq})
	_ = reg.Collect("chrony.residual_frequency", now, map[string]float64{"residual_frequency": s.residual})
	_ = reg.Collect("chrony.skew", now, map[string]float64{"skew": s.skew})
	_ = reg.Collect("chrony.update_interval", now, map[string]float64{"update_interval": s.update})
	_ = reg.Collect("chrony.leap_status", now, chronyLeap(s.leap))
	_ = reg.Collect("chrony.activity", now, map[string]float64{
		"online": s.online, "offline": s.offline, "burst_online": s.burstOn, "burst_offline": s.burstOff, "unresolved": s.unresolved})
	return nil
}

func (c *chronyCollector) stats(ctx context.Context) (chronySample, error) {
	tr, act, err := c.sample(ctx)
	if err != nil {
		return chronySample{}, err
	}
	s := parseChronyTracking(tr)
	parseChronyActivity(act, &s)
	if s.stratum == 0 && s.leap == "" && !strings.Contains(tr, "Stratum") {
		return chronySample{}, fmt.Errorf("chrony: unexpected tracking output")
	}
	return s, nil
}

func (c *chronyCollector) sample(ctx context.Context) (string, string, error) {
	run := c.run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
			defer cancel()
			cmd := exec.CommandContext(cctx, name, args...)
			cmd.WaitDelay = execWaitDelay
			return cmd.Output()
		}
	}
	tr, err := run(ctx, c.cfg.Command, "-n", "tracking")
	if err != nil {
		return "", "", fmt.Errorf("chronyc tracking: %w", err)
	}
	act, err := run(ctx, c.cfg.Command, "-n", "activity")
	if err != nil {
		act = nil
	}
	return string(tr), string(act), nil
}

func parseChronyTracking(s string) chronySample {
	out := chronySample{}
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		num := firstFloat(v)
		switch k {
		case "Stratum":
			out.stratum = num
		case "System time":
			out.correction = num
			if strings.Contains(v, "slow") {
				out.correction = -out.correction
			}
		case "Last offset":
			out.lastOff = signedFloat(v)
		case "RMS offset":
			out.rmsOff = num
		case "Root delay":
			out.rootDelay = num
		case "Root dispersion":
			out.rootDisp = num
		case "Frequency":
			out.freq = num
			if strings.Contains(v, "slow") {
				out.freq = -out.freq
			}
		case "Residual freq":
			out.residual = signedFloat(v)
		case "Skew":
			out.skew = num
		case "Update interval":
			out.update = num
		case "Leap status":
			out.leap = strings.ToLower(v)
		}
	}
	return out
}

func parseChronyActivity(s string, out *chronySample) {
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		n, err := strconv.ParseFloat(f[0], 64)
		if err != nil {
			continue
		}
		switch {
		case strings.Contains(line, "online") && !strings.Contains(line, "burst"):
			out.online = n
		case strings.Contains(line, "offline") && !strings.Contains(line, "burst"):
			out.offline = n
		case strings.Contains(line, "burst") && strings.Contains(line, "online"):
			out.burstOn = n
		case strings.Contains(line, "burst") && strings.Contains(line, "offline"):
			out.burstOff = n
		case strings.Contains(line, "unknown"):
			out.unresolved = n
		}
	}
}

func chronyLeap(s string) map[string]float64 {
	out := map[string]float64{"normal": 0, "insert_second": 0, "delete_second": 0, "unsynchronised": 0}
	switch {
	case strings.Contains(s, "insert"):
		out["insert_second"] = 1
	case strings.Contains(s, "delete"):
		out["delete_second"] = 1
	case strings.Contains(s, "unsychron") || strings.Contains(s, "unsynchron"):
		out["unsynchronised"] = 1
	default:
		out["normal"] = 1
	}
	return out
}

func firstFloat(s string) float64 {
	var b strings.Builder
	started := false
	for _, r := range s {
		if r == '+' || r == '-' || r == '.' || (r >= '0' && r <= '9') {
			started = true
			b.WriteRune(r)
			continue
		}
		if started {
			break
		}
	}
	v, _ := strconv.ParseFloat(b.String(), 64)
	return v
}

func signedFloat(s string) float64 {
	s = strings.TrimSpace(s)
	neg := strings.HasPrefix(s, "-") || strings.Contains(strings.ToLower(s), "slow")
	v := firstFloat(s)
	if neg && v > 0 && !strings.HasPrefix(strings.TrimSpace(s), "-") && !strings.HasPrefix(strings.TrimSpace(s), "+") {
		return -v
	}
	if strings.HasPrefix(strings.TrimSpace(s), "-") {
		return -firstFloat(strings.TrimPrefix(strings.TrimSpace(s), "-"))
	}
	if strings.HasPrefix(strings.TrimSpace(s), "+") {
		return firstFloat(s)
	}
	return v
}
