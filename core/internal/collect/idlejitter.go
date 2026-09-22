package collect

import (
	"context"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// idlejitterConfig is collectors.modules.idlejitter (Netdata idlejitter.plugin).
type idlejitterConfig struct {
	Sleep time.Duration `yaml:"sleep"`
	Loops int           `yaml:"loops"`
}

type idlejitterCollector struct {
	cfg   idlejitterConfig
	sleep func(d time.Duration) time.Duration
}

func init() {
	Register("idlejitter", func() Collector { return &idlejitterCollector{} })
}

func (i *idlejitterCollector) Name() string { return "idlejitter" }

func (i *idlejitterCollector) Configure(decode func(v any) error) error {
	if err := decode(&i.cfg); err != nil {
		return err
	}
	if i.cfg.Sleep <= 0 {
		i.cfg.Sleep = 20 * time.Millisecond
	}
	if i.cfg.Loops <= 0 {
		i.cfg.Loops = 4
	}
	return nil
}

func (i *idlejitterCollector) Init(reg *registry.Registry) error {
	if i.cfg.Loops == 0 && i.cfg.Sleep == 0 {
		if err := i.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if i.sleep == nil {
		i.sleep = func(d time.Duration) time.Duration {
			start := time.Now()
			time.Sleep(d)
			return time.Since(start)
		}
	}
	ch := sysChart("system.idlejitter", "idlejitter", "CPU idle jitter", "microseconds", 250,
		&registry.Dimension{ID: "min"}, &registry.Dimension{ID: "max"}, &registry.Dimension{ID: "average"})
	ch.Plugin, ch.Module = "idlejitter", "idlejitter"
	reg.AddChart(ch)
	return nil
}

func (i *idlejitterCollector) Collect(_ context.Context, reg *registry.Registry, now time.Time) error {
	want := i.cfg.Sleep
	if want <= 0 {
		want = time.Millisecond
	}
	var minUS, maxUS, sumUS float64
	n := i.cfg.Loops
	if n <= 0 {
		n = 1
	}
	for k := 0; k < n; k++ {
		dt := i.sleep(want)
		errUS := float64((dt - want).Microseconds())
		if errUS < 0 {
			errUS = 0
		}
		if k == 0 || errUS < minUS {
			minUS = errUS
		}
		if errUS > maxUS {
			maxUS = errUS
		}
		sumUS += errUS
	}
	_ = reg.Collect("system.idlejitter", now, map[string]float64{
		"min": minUS, "max": maxUS, "average": sumUS / float64(n),
	})
	return nil
}
