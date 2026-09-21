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

// scaleioConfig is collectors.modules.scaleio (VxFlex OS Gateway API).
type scaleioConfig struct {
	URL      string        `yaml:"url"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type scaleioCollector struct {
	cfg    scaleioConfig
	client *http.Client
	url    string
	get    func(ctx context.Context, path string) ([]byte, error)
}

func init() {
	Register("scaleio", func() Collector { return &scaleioCollector{} })
}

func (s *scaleioCollector) Name() string { return "scaleio" }

func (s *scaleioCollector) Configure(decode func(v any) error) error {
	if err := decode(&s.cfg); err != nil {
		return err
	}
	if s.cfg.Timeout <= 0 {
		s.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (s *scaleioCollector) Init(reg *registry.Registry) error {
	if s.cfg.Timeout <= 0 {
		if err := s.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	s.client = insecureClient(s.cfg.Timeout)
	base := strings.TrimRight(s.cfg.URL, "/")
	if base == "" {
		base = "https://127.0.0.1"
	}
	s.url = base
	if _, err := s.system(context.Background()); err != nil {
		return err
	}
	for _, ch := range []*registry.Chart{
		{ID: "scaleio.system_capacity", Title: "System capacity", Units: "KiB", Type: registry.Stacked, Priority: 61800,
			Dimensions: []*registry.Dimension{{ID: "max_capacity"}, {ID: "unused"}}},
		{ID: "scaleio.system_workload", Title: "System workload", Units: "iops", Priority: 61810,
			Dimensions: []*registry.Dimension{{ID: "total"}}},
		{ID: "scaleio.system_defined_components", Title: "Components", Units: "components", Priority: 61820,
			Dimensions: []*registry.Dimension{{ID: "sdc"}, {ID: "sds"}, {ID: "volumes"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "scaleio", "scaleio", "scaleio"
		reg.AddChart(ch)
	}
	return nil
}

func (s *scaleioCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := s.system(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("scaleio.system_capacity", now, map[string]float64{
		"max_capacity": nestFloat(m, "maxCapacityInKb"), "unused": nestFloat(m, "capacityInUseInKb"),
	})
	if nestFloat(m, "maxCapacityInKb") == 0 {
		_ = reg.Collect("scaleio.system_capacity", now, map[string]float64{
			"max_capacity": nestFloat(m, "max_capacity"), "unused": nestFloat(m, "unused"),
		})
	}
	iops := nestFloat(m, "totalIosInProgress")
	if iops == 0 {
		iops = nestFloat(m, "total")
	}
	_ = reg.Collect("scaleio.system_workload", now, map[string]float64{"total": iops})
	_ = reg.Collect("scaleio.system_defined_components", now, map[string]float64{
		"sdc": nestFloat(m, "numOfSdcs"), "sds": nestFloat(m, "numOfSds"), "volumes": nestFloat(m, "numOfVolumes"),
	})
	return nil
}

func (s *scaleioCollector) system(ctx context.Context) (map[string]any, error) {
	b, err := s.fetch(ctx, "/api/types/System/instances")
	if err != nil {
		return nil, err
	}
	if m, err := jsonMap(b); err == nil {
		if nestFloat(m, "maxCapacityInKb") > 0 || nestFloat(m, "numOfSds") > 0 || nestString(m, "id") != "" {
			return m, nil
		}
	}
	var arr []map[string]any
	if err := json.Unmarshal(b, &arr); err == nil && len(arr) > 0 {
		return arr[0], nil
	}
	return nil, fmt.Errorf("scaleio: unexpected response")
}

func (s *scaleioCollector) fetch(ctx context.Context, path string) ([]byte, error) {
	if s.get != nil {
		return s.get(ctx, path)
	}
	token, err := httpGetAuth(ctx, s.client, s.url+"/api/login", s.cfg.User, s.cfg.Password)
	if err != nil {
		return nil, fmt.Errorf("scaleio: %w", err)
	}
	tok := strings.Trim(strings.TrimSpace(string(token)), `"`)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url+path, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(s.cfg.User, tok)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scaleio: %w", err)
	}
	defer resp.Body.Close()
	b, err := readBody(resp)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("scaleio: %s", resp.Status)
	}
	return b, nil
}
