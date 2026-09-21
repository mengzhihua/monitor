package collect

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// typesenseConfig is collectors.modules.typesense (HTTP /health).
type typesenseConfig struct {
	URL     string        `yaml:"url"`
	APIKey  string        `yaml:"api_key"`
	Timeout time.Duration `yaml:"timeout"`
}

type typesenseCollector struct {
	cfg    typesenseConfig
	client *http.Client
	url    string
}

func init() {
	Register("typesense", func() Collector { return &typesenseCollector{} })
}

func (t *typesenseCollector) Name() string { return "typesense" }

func (t *typesenseCollector) Configure(decode func(v any) error) error {
	if err := decode(&t.cfg); err != nil {
		return err
	}
	if t.cfg.Timeout <= 0 {
		t.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (t *typesenseCollector) Init(reg *registry.Registry) error {
	if t.cfg.Timeout <= 0 {
		if err := t.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	t.client = &http.Client{Timeout: t.cfg.Timeout}
	base := strings.TrimRight(t.cfg.URL, "/")
	if base == "" {
		base = "http://127.0.0.1:8108"
	}
	t.url = base
	if _, err := t.health(context.Background()); err != nil {
		return err
	}
	ch := &registry.Chart{ID: "typesense.health_status", Title: "Health Status", Units: "status", Priority: 58300,
		Dimensions: []*registry.Dimension{{ID: "ok"}, {ID: "out_of_disk"}, {ID: "out_of_memory"}}}
	ch.Family, ch.Plugin, ch.Module = "typesense", "typesense", "typesense"
	reg.AddChart(ch)
	return nil
}

func (t *typesenseCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := t.health(ctx)
	if err != nil {
		return err
	}
	ok := 0.0
	switch v := m["ok"].(type) {
	case bool:
		if v {
			ok = 1
		}
	default:
		if nestFloat(m, "ok") != 0 || strings.EqualFold(nestString(m, "ok"), "true") {
			ok = 1
		}
	}
	ood, oom := 0.0, 0.0
	if ok < 1 {
		st := strings.ToLower(nestString(m, "error") + " " + nestString(m, "status"))
		switch {
		case strings.Contains(st, "disk"):
			ood = 1
		case strings.Contains(st, "memory"):
			oom = 1
		}
	}
	_ = reg.Collect("typesense.health_status", now, map[string]float64{"ok": ok, "out_of_disk": ood, "out_of_memory": oom})
	return nil
}

func (t *typesenseCollector) health(ctx context.Context) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.url+"/health", nil)
	if err != nil {
		return nil, err
	}
	if t.cfg.APIKey != "" {
		req.Header.Set("X-TYPESENSE-API-KEY", t.cfg.APIKey)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("typesense: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("typesense: %s", resp.Status)
	}
	m, err := jsonMap(b)
	if err != nil {
		return nil, fmt.Errorf("typesense: %w", err)
	}
	if _, ok := m["ok"]; !ok {
		return nil, fmt.Errorf("typesense: no health")
	}
	return m, nil
}
