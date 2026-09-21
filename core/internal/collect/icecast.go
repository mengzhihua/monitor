package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// icecastConfig is collectors.modules.icecast (status-json.xsl).
type icecastConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type icecastCollector struct {
	cfg    icecastConfig
	client *http.Client
	url    string
	seen   map[string]bool
}

func init() {
	Register("icecast", func() Collector { return &icecastCollector{} })
}

func (i *icecastCollector) Name() string { return "icecast" }

func (i *icecastCollector) Configure(decode func(v any) error) error {
	if err := decode(&i.cfg); err != nil {
		return err
	}
	if i.cfg.Timeout <= 0 {
		i.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (i *icecastCollector) Init(reg *registry.Registry) error {
	if i.cfg.Timeout <= 0 {
		if err := i.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	i.client = &http.Client{Timeout: i.cfg.Timeout}
	i.seen = map[string]bool{}
	base := strings.TrimRight(i.cfg.URL, "/")
	if base == "" {
		base = "http://127.0.0.1:8000"
	}
	i.url = base
	if _, err := i.sources(context.Background()); err != nil {
		return err
	}
	return nil
}

func (i *icecastCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	srcs, err := i.sources(ctx)
	if err != nil {
		return err
	}
	for name, n := range srcs {
		id := sanitizeID(name)
		if !i.seen[id] {
			i.seen[id] = true
			ch := &registry.Chart{ID: "icecast.listeners." + id, Context: "icecast.listeners", Title: "Icecast Listeners", Units: "listeners", Priority: 59600,
				Dimensions: []*registry.Dimension{{ID: "listeners"}}}
			ch.Family, ch.Plugin, ch.Module = "icecast", "icecast", "icecast"
			reg.AddChart(ch)
		}
		_ = reg.Collect("icecast.listeners."+id, now, map[string]float64{"listeners": n})
	}
	return nil
}

func (i *icecastCollector) sources(ctx context.Context) (map[string]float64, error) {
	b, err := httpGet(ctx, i.client, i.url+"/status-json.xsl")
	if err != nil {
		b, err = httpGet(ctx, i.client, i.url+"/status-json.xsl?mount=/")
		if err != nil {
			return nil, fmt.Errorf("icecast: %w", err)
		}
	}
	m, err := jsonMap(b)
	if err != nil {
		return nil, fmt.Errorf("icecast: %w", err)
	}
	stats := nestMap(m, "icestats")
	if stats == nil {
		return nil, fmt.Errorf("icecast: no icestats")
	}
	out := map[string]float64{}
	switch src := stats["source"].(type) {
	case []any:
		for _, v := range src {
			mm, _ := v.(map[string]any)
			name := nestString(mm, "listenurl")
			if name == "" {
				name = nestString(mm, "server_name")
			}
			if name == "" {
				name = "source"
			}
			out[name] = nestFloat(mm, "listeners")
		}
	case map[string]any:
		name := nestString(src, "listenurl")
		if name == "" {
			name = "source"
		}
		out[name] = nestFloat(src, "listeners")
	}
	if len(out) == 0 {
		out["source"] = nestFloat(stats, "listeners")
	}
	return out, nil
}
