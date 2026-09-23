package collect

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// smbiosMemoryConfig is collectors.modules.smbios_memory (`dmidecode -t memory`).
type smbiosMemoryConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type smbiosDIMM struct {
	locator     string
	size, speed float64
}

type smbiosMemoryCollector struct {
	cfg  smbiosMemoryConfig
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	seen map[string]bool
	last time.Time
}

const smbiosEvery = 5 * time.Minute

func init() {
	Register("smbios_memory", func() Collector { return &smbiosMemoryCollector{} })
}

func (s *smbiosMemoryCollector) Name() string { return "smbios_memory" }

func (s *smbiosMemoryCollector) Configure(decode func(v any) error) error {
	if err := decode(&s.cfg); err != nil {
		return err
	}
	if s.cfg.Command == "" {
		s.cfg.Command = "dmidecode"
	}
	if s.cfg.Timeout <= 0 {
		s.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (s *smbiosMemoryCollector) Init(reg *registry.Registry) error {
	if s.cfg.Command == "" {
		if err := s.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if s.run == nil {
		s.run = execRun(s.cfg.Timeout)
	}
	s.seen = map[string]bool{}
	dimms, err := s.dimms(context.Background())
	if err != nil {
		return err
	}
	if len(dimms) == 0 {
		return fmt.Errorf("smbios_memory: no modules")
	}
	return nil
}

func (s *smbiosMemoryCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	if !sampleDue(&s.last, now, smbiosEvery) {
		return nil
	}
	dimms, err := s.dimms(ctx)
	if err != nil {
		return err
	}
	for _, d := range dimms {
		id := sanitizeID(d.locator)
		if !s.seen[id] {
			s.seen[id] = true
			for _, ch := range []*registry.Chart{
				{ID: "smbios.memory.size." + id, Context: "smbios.memory.size", Title: "Memory module size", Units: "bytes", Priority: 61900,
					Dimensions: []*registry.Dimension{{ID: "size"}}},
				{ID: "smbios.memory.speed." + id, Context: "smbios.memory.speed", Title: "Memory module speed", Units: "MT/s", Priority: 61910,
					Dimensions: []*registry.Dimension{{ID: "speed"}}},
			} {
				ch.Family, ch.Plugin, ch.Module = "smbios_memory", "smbios_memory", "smbios_memory"
				reg.AddChart(ch)
			}
		}
		_ = reg.Collect("smbios.memory.size."+id, now, map[string]float64{"size": d.size})
		_ = reg.Collect("smbios.memory.speed."+id, now, map[string]float64{"speed": d.speed})
	}
	return nil
}

func (s *smbiosMemoryCollector) dimms(ctx context.Context) ([]smbiosDIMM, error) {
	b, err := s.run(ctx, s.cfg.Command, "-t", "memory")
	if err != nil {
		return nil, fmt.Errorf("smbios_memory: %w", err)
	}
	dimms := parseSMBIOSMemory(string(b))
	if len(dimms) == 0 {
		return nil, fmt.Errorf("smbios_memory: no modules")
	}
	return dimms, nil
}

func parseSMBIOSMemory(s string) []smbiosDIMM {
	var out []smbiosDIMM
	var cur smbiosDIMM
	in := false
	flush := func() {
		if in && cur.size > 0 {
			if cur.locator == "" {
				cur.locator = fmt.Sprintf("dimm%d", len(out))
			}
			out = append(out, cur)
		}
		cur = smbiosDIMM{}
		in = false
	}
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "Memory Device" {
			flush()
			in = true
			continue
		}
		if !in {
			continue
		}
		switch {
		case strings.HasPrefix(t, "Locator:"):
			loc := strings.TrimSpace(strings.TrimPrefix(t, "Locator:"))
			if loc != "" && !strings.EqualFold(loc, "None") && !strings.EqualFold(loc, "Not Specified") {
				cur.locator = loc
			}
		case strings.HasPrefix(t, "Size:"):
			v := strings.TrimSpace(strings.TrimPrefix(t, "Size:"))
			if strings.EqualFold(v, "No Module Installed") || strings.EqualFold(v, "Not Installed") {
				cur.size = 0
				continue
			}
			n := firstFloat(v)
			switch {
			case strings.Contains(strings.ToUpper(v), "GB"):
				cur.size = n * 1073741824
			case strings.Contains(strings.ToUpper(v), "MB"):
				cur.size = n * 1048576
			case strings.Contains(strings.ToUpper(v), "KB"):
				cur.size = n * 1024
			default:
				cur.size = n
			}
		case strings.HasPrefix(t, "Speed:"):
			cur.speed = firstFloat(t)
		}
	}
	flush()
	return out
}
