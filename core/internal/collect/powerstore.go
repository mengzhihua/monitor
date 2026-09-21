package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// powerstoreConfig is collectors.modules.powerstore (Dell PowerStore REST).
type powerstoreConfig struct {
	URL      string        `yaml:"url"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type powerstoreCollector struct {
	cfg    powerstoreConfig
	client *http.Client
	url    string
}

func init() {
	Register("powerstore", func() Collector { return &powerstoreCollector{} })
}

func (p *powerstoreCollector) Name() string { return "powerstore" }

func (p *powerstoreCollector) Configure(decode func(v any) error) error {
	if err := decode(&p.cfg); err != nil {
		return err
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (p *powerstoreCollector) Init(reg *registry.Registry) error {
	if p.cfg.Timeout <= 0 {
		if err := p.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	p.client = insecureClient(p.cfg.Timeout)
	base := strings.TrimRight(p.cfg.URL, "/")
	if base == "" {
		base = "https://127.0.0.1"
	}
	p.url = base
	if _, err := p.cluster(context.Background()); err != nil {
		return err
	}
	for _, ch := range []*registry.Chart{
		{ID: "powerstore.cluster_health", Title: "Cluster health", Units: "status", Priority: 61500,
			Dimensions: []*registry.Dimension{{ID: "healthy"}}},
		{ID: "powerstore.capacity_used", Title: "Capacity used", Units: "bytes", Type: registry.Area, Priority: 61510,
			Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "total"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "powerstore", "powerstore", "powerstore"
		reg.AddChart(ch)
	}
	return nil
}

func (p *powerstoreCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := p.cluster(ctx)
	if err != nil {
		return err
	}
	healthy := 0.0
	st := strings.ToLower(nestString(m, "state") + nestString(m, "health_details", "state") + nestString(m, "global_health"))
	if st == "" {
		st = strings.ToLower(fmt.Sprint(m["state"]))
	}
	if strings.Contains(st, "good") || strings.Contains(st, "ok") || strings.Contains(st, "healthy") || strings.Contains(st, "normal") {
		healthy = 1
	}
	if nestFloat(m, "healthy") == 1 {
		healthy = 1
	}
	_ = reg.Collect("powerstore.cluster_health", now, map[string]float64{"healthy": healthy})
	used := nestFloat(m, "physical_used")
	if used == 0 {
		used = nestFloat(m, "size_used")
	}
	total := nestFloat(m, "physical_total")
	if total == 0 {
		total = nestFloat(m, "size_total")
	}
	_ = reg.Collect("powerstore.capacity_used", now, map[string]float64{"used": used, "total": total})
	return nil
}

func (p *powerstoreCollector) cluster(ctx context.Context) (map[string]any, error) {
	b, err := httpGetAuth(ctx, p.client, p.url+"/api/rest/cluster", p.cfg.User, p.cfg.Password)
	if err != nil {
		return nil, fmt.Errorf("powerstore: %w", err)
	}
	if m, err := jsonMap(b); err == nil {
		if nestString(m, "id") != "" || nestString(m, "state") != "" || nestFloat(m, "physical_used") > 0 || nestFloat(m, "healthy") > 0 {
			return m, nil
		}
	}
	var arr []any
	if err := json.Unmarshal(b, &arr); err == nil && len(arr) > 0 {
		if mm, ok := arr[0].(map[string]any); ok {
			return mm, nil
		}
	}
	return nil, fmt.Errorf("powerstore: unexpected response")
}
