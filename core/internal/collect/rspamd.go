package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// rspamdConfig is collectors.modules.rspamd (HTTP /stat).
type rspamdConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type rspamdCollector struct {
	cfg    rspamdConfig
	client *http.Client
	url    string
}

func init() {
	Register("rspamd", func() Collector { return &rspamdCollector{} })
}

func (r *rspamdCollector) Name() string { return "rspamd" }

func (r *rspamdCollector) Configure(decode func(v any) error) error {
	if err := decode(&r.cfg); err != nil {
		return err
	}
	if r.cfg.Timeout <= 0 {
		r.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (r *rspamdCollector) Init(reg *registry.Registry) error {
	if r.cfg.Timeout <= 0 {
		if err := r.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	r.client = &http.Client{Timeout: r.cfg.Timeout}
	base := strings.TrimRight(r.cfg.URL, "/")
	if base == "" {
		base = "http://127.0.0.1:11334"
	}
	r.url = base
	if _, err := r.stat(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "rspamd.classifications", Title: "Classifications", Units: "messages/s", Type: registry.Stacked, Priority: 58200,
			Dimensions: []*registry.Dimension{{ID: "ham", Algorithm: inc}, {ID: "spam", Algorithm: inc}}},
		{ID: "rspamd.actions", Title: "Actions", Units: "messages/s", Type: registry.Stacked, Priority: 58210,
			Dimensions: []*registry.Dimension{
				{ID: "reject", Algorithm: inc}, {ID: "soft_reject", Algorithm: inc}, {ID: "rewrite_subject", Algorithm: inc},
				{ID: "add_header", Algorithm: inc}, {ID: "greylist", Algorithm: inc}, {ID: "no_action", Algorithm: inc},
			}},
		{ID: "rspamd.scans", Title: "Scanned messages", Units: "messages/s", Priority: 58220,
			Dimensions: []*registry.Dimension{{ID: "scanned", Algorithm: inc}}},
		{ID: "rspamd.learns", Title: "Learned messages", Units: "messages/s", Priority: 58230,
			Dimensions: []*registry.Dimension{{ID: "learned", Algorithm: inc}}},
		{ID: "rspamd.connections", Title: "Connections", Units: "connections/s", Priority: 58240,
			Dimensions: []*registry.Dimension{{ID: "connections", Algorithm: inc}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "rspamd", "rspamd", "rspamd"
		reg.AddChart(ch)
	}
	return nil
}

func (r *rspamdCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := r.stat(ctx)
	if err != nil {
		return err
	}
	actions := nestMap(m, "actions")
	_ = reg.Collect("rspamd.classifications", now, map[string]float64{"ham": nestFloat(m, "ham_count"), "spam": nestFloat(m, "spam_count")})
	_ = reg.Collect("rspamd.actions", now, map[string]float64{
		"reject": nestFloat(actions, "reject"), "soft_reject": nestFloat(actions, "soft reject"),
		"rewrite_subject": nestFloat(actions, "rewrite subject"), "add_header": nestFloat(actions, "add header"),
		"greylist": nestFloat(actions, "greylist"), "no_action": nestFloat(actions, "no action"),
	})
	_ = reg.Collect("rspamd.scans", now, map[string]float64{"scanned": nestFloat(m, "scanned")})
	_ = reg.Collect("rspamd.learns", now, map[string]float64{"learned": nestFloat(m, "learned")})
	_ = reg.Collect("rspamd.connections", now, map[string]float64{"connections": nestFloat(m, "connections")})
	return nil
}

func (r *rspamdCollector) stat(ctx context.Context) (map[string]any, error) {
	b, err := httpGet(ctx, r.client, r.url+"/stat")
	if err != nil {
		return nil, fmt.Errorf("rspamd: %w", err)
	}
	m, err := jsonMap(b)
	if err != nil {
		return nil, fmt.Errorf("rspamd: %w", err)
	}
	if _, ok := m["scanned"]; !ok {
		return nil, fmt.Errorf("rspamd: no stats")
	}
	return m, nil
}
