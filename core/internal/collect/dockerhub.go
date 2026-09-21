package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// dockerhubConfig is collectors.modules.dockerhub (Hub v2 repositories API).
type dockerhubConfig struct {
	URL          string        `yaml:"url"`
	Repositories []string      `yaml:"repositories"`
	Timeout      time.Duration `yaml:"timeout"`
}

type dockerhubCollector struct {
	cfg    dockerhubConfig
	client *http.Client
	url    string
}

func init() {
	Register("dockerhub", func() Collector { return &dockerhubCollector{} })
}

func (d *dockerhubCollector) Name() string { return "dockerhub" }

func (d *dockerhubCollector) Configure(decode func(v any) error) error {
	if err := decode(&d.cfg); err != nil {
		return err
	}
	if d.cfg.Timeout <= 0 {
		d.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (d *dockerhubCollector) Init(reg *registry.Registry) error {
	if d.cfg.Timeout <= 0 {
		if err := d.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if len(d.cfg.Repositories) == 0 {
		return fmt.Errorf("dockerhub: no repositories")
	}
	d.client = &http.Client{Timeout: d.cfg.Timeout}
	d.url = strings.TrimRight(d.cfg.URL, "/")
	if d.url == "" {
		d.url = "https://hub.docker.com/v2/repositories"
	}
	if _, err := d.repos(context.Background()); err != nil {
		return err
	}
	pullDims := make([]*registry.Dimension, 0, len(d.cfg.Repositories))
	starDims := make([]*registry.Dimension, 0, len(d.cfg.Repositories))
	stDims := make([]*registry.Dimension, 0, len(d.cfg.Repositories))
	luDims := make([]*registry.Dimension, 0, len(d.cfg.Repositories))
	inc := registry.Incremental
	for _, repo := range d.cfg.Repositories {
		id := sanitizeID(strings.ReplaceAll(repo, "/", "_"))
		pullDims = append(pullDims, &registry.Dimension{ID: id, Algorithm: inc})
		starDims = append(starDims, &registry.Dimension{ID: id})
		stDims = append(stDims, &registry.Dimension{ID: id})
		luDims = append(luDims, &registry.Dimension{ID: id})
	}
	for _, ch := range []*registry.Chart{
		{ID: "dockerhub.pulls_sum", Title: "Pulls Summary", Units: "pulls", Priority: 60900,
			Dimensions: []*registry.Dimension{{ID: "sum"}}},
		{ID: "dockerhub.pulls", Title: "Pulls", Units: "pulls", Type: registry.Stacked, Priority: 60910, Dimensions: starDims},
		{ID: "dockerhub.pulls_rate", Title: "Pulls Rate", Units: "pulls/s", Type: registry.Stacked, Priority: 60920, Dimensions: pullDims},
		{ID: "dockerhub.stars", Title: "Stars", Units: "stars", Type: registry.Stacked, Priority: 60930, Dimensions: cloneDims(starDims)},
		{ID: "dockerhub.status", Title: "Current Status", Units: "status", Priority: 60940, Dimensions: stDims},
		{ID: "dockerhub.last_updated", Title: "Time Since Last Updated", Units: "seconds", Priority: 60950, Dimensions: luDims},
	} {
		ch.Family, ch.Plugin, ch.Module = "dockerhub", "dockerhub", "dockerhub"
		reg.AddChart(ch)
	}
	return nil
}

func cloneDims(in []*registry.Dimension) []*registry.Dimension {
	out := make([]*registry.Dimension, len(in))
	for i, d := range in {
		cp := *d
		out[i] = &cp
	}
	return out
}

func (d *dockerhubCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	repos, err := d.repos(ctx)
	if err != nil {
		return err
	}
	pulls, stars, status, updated := map[string]float64{}, map[string]float64{}, map[string]float64{}, map[string]float64{}
	var sum float64
	for repo, m := range repos {
		id := sanitizeID(strings.ReplaceAll(repo, "/", "_"))
		p := nestFloat(m, "pull_count")
		pulls[id] = p
		sum += p
		stars[id] = nestFloat(m, "star_count")
		if strings.EqualFold(nestString(m, "status"), "active") || nestString(m, "status") == "" {
			status[id] = 1
		}
		if ts := nestString(m, "last_updated"); ts != "" {
			if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				updated[id] = now.Sub(t).Seconds()
			} else if t, err := time.Parse(time.RFC3339, ts); err == nil {
				updated[id] = now.Sub(t).Seconds()
			}
		}
	}
	_ = reg.Collect("dockerhub.pulls_sum", now, map[string]float64{"sum": sum})
	_ = reg.Collect("dockerhub.pulls", now, pulls)
	_ = reg.Collect("dockerhub.pulls_rate", now, pulls)
	_ = reg.Collect("dockerhub.stars", now, stars)
	_ = reg.Collect("dockerhub.status", now, status)
	_ = reg.Collect("dockerhub.last_updated", now, updated)
	return nil
}

func (d *dockerhubCollector) repos(ctx context.Context) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	for _, repo := range d.cfg.Repositories {
		b, err := httpGet(ctx, d.client, d.url+"/"+strings.Trim(repo, "/"))
		if err != nil {
			return nil, fmt.Errorf("dockerhub: %w", err)
		}
		m, err := jsonMap(b)
		if err != nil {
			return nil, fmt.Errorf("dockerhub: %w", err)
		}
		if _, ok := m["pull_count"]; !ok && nestFloat(m, "star_count") == 0 && nestString(m, "name") == "" {
			return nil, fmt.Errorf("dockerhub: unexpected response for %s", repo)
		}
		out[repo] = m
	}
	return out, nil
}
