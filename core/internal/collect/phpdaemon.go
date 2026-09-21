package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// phpdaemonConfig is collectors.modules.phpdaemon (FullStatus JSON).
type phpdaemonConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type phpdaemonCollector struct {
	cfg    phpdaemonConfig
	client *http.Client
	url    string
}

func init() {
	Register("phpdaemon", func() Collector { return &phpdaemonCollector{} })
}

func (p *phpdaemonCollector) Name() string { return "phpdaemon" }

func (p *phpdaemonCollector) Configure(decode func(v any) error) error {
	if err := decode(&p.cfg); err != nil {
		return err
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (p *phpdaemonCollector) Init(reg *registry.Registry) error {
	if p.cfg.Timeout <= 0 {
		if err := p.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	p.client = &http.Client{Timeout: p.cfg.Timeout}
	base := strings.TrimRight(p.cfg.URL, "/")
	if base == "" {
		base = "http://127.0.0.1:8509/FullStatus"
	}
	p.url = base
	if _, err := p.status(context.Background()); err != nil {
		return err
	}
	for _, ch := range []*registry.Chart{
		{ID: "phpdaemon.workers", Title: "Workers", Units: "workers", Type: registry.Stacked, Priority: 59700,
			Dimensions: []*registry.Dimension{{ID: "alive"}, {ID: "shutdown"}}},
		{ID: "phpdaemon.alive_workers", Title: "Alive Workers State", Units: "workers", Type: registry.Stacked, Priority: 59710,
			Dimensions: []*registry.Dimension{{ID: "idle"}, {ID: "busy"}, {ID: "reloading"}}},
		{ID: "phpdaemon.idle_workers", Title: "Idle Workers State", Units: "workers", Type: registry.Stacked, Priority: 59720,
			Dimensions: []*registry.Dimension{{ID: "preinit"}, {ID: "init"}, {ID: "initialized"}}},
		{ID: "phpdaemon.uptime", Title: "Uptime", Units: "seconds", Priority: 59730,
			Dimensions: []*registry.Dimension{{ID: "time"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "phpdaemon", "phpdaemon", "phpdaemon"
		reg.AddChart(ch)
	}
	return nil
}

func (p *phpdaemonCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := p.status(ctx)
	if err != nil {
		return err
	}
	w := nestMap(m, "workers")
	if w == nil {
		w = m
	}
	alive := nestMap(w, "alive")
	aliveN := nestFloat(w, "alive-count")
	if aliveN == 0 {
		aliveN = nestFloat(alive, "idle") + nestFloat(alive, "busy") + nestFloat(alive, "reloading")
	}
	_ = reg.Collect("phpdaemon.workers", now, map[string]float64{"alive": aliveN, "shutdown": nestFloat(w, "shutdown")})
	_ = reg.Collect("phpdaemon.alive_workers", now, map[string]float64{"idle": nestFloat(alive, "idle"), "busy": nestFloat(alive, "busy"), "reloading": nestFloat(alive, "reloading")})
	idle := nestMap(alive, "idle-states")
	if idle == nil {
		idle = nestMap(m, "idle")
	}
	_ = reg.Collect("phpdaemon.idle_workers", now, map[string]float64{"preinit": nestFloat(idle, "preinit"), "init": nestFloat(idle, "init"), "initialized": nestFloat(idle, "initialized")})
	_ = reg.Collect("phpdaemon.uptime", now, map[string]float64{"time": nestFloat(m, "uptime")})
	return nil
}

func (p *phpdaemonCollector) status(ctx context.Context) (map[string]any, error) {
	b, err := httpGet(ctx, p.client, p.url)
	if err != nil {
		return nil, fmt.Errorf("phpdaemon: %w", err)
	}
	m, err := jsonMap(b)
	if err != nil {
		return nil, fmt.Errorf("phpdaemon: %w", err)
	}
	if nestMap(m, "workers") == nil && nestFloat(m, "uptime") == 0 && nestMap(m, "alive") == nil {
		return nil, fmt.Errorf("phpdaemon: no status")
	}
	return m, nil
}
