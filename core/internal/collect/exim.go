package collect

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// eximConfig is collectors.modules.exim (exim -bpc).
type eximConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type eximCollector struct {
	cfg eximConfig
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func init() {
	Register("exim", func() Collector { return &eximCollector{} })
}

func (e *eximCollector) Name() string { return "exim" }

func (e *eximCollector) Configure(decode func(v any) error) error {
	if err := decode(&e.cfg); err != nil {
		return err
	}
	if e.cfg.Command == "" {
		e.cfg.Command = "exim"
	}
	if e.cfg.Timeout <= 0 {
		e.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (e *eximCollector) Init(reg *registry.Registry) error {
	if e.cfg.Command == "" {
		if err := e.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := e.count(context.Background()); err != nil {
		return err
	}
	c := &registry.Chart{ID: "exim.qemails", Title: "Exim Queue Emails", Units: "emails", Priority: 52100,
		Dimensions: []*registry.Dimension{{ID: "emails"}}}
	c.Family, c.Plugin, c.Module = "exim", "exim", "exim"
	reg.AddChart(c)
	return nil
}

func (e *eximCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	n, err := e.count(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("exim.qemails", now, map[string]float64{"emails": n})
	return nil
}

func (e *eximCollector) count(ctx context.Context) (float64, error) {
	run := e.run
	if run == nil {
		run = execRun(e.cfg.Timeout)
	}
	out, err := run(ctx, e.cfg.Command, "-bpc")
	if err != nil {
		return 0, fmt.Errorf("exim: %w", err)
	}
	s := strings.TrimSpace(string(out))
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("exim: unexpected %q", s)
	}
	return n, nil
}
