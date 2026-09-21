package collect

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// tengineConfig is collectors.modules.tengine (ngx_http_reqstat_module /us).
type tengineConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type tengineCollector struct {
	cfg    tengineConfig
	client *http.Client
	url    string
}

func init() {
	Register("tengine", func() Collector { return &tengineCollector{} })
}

func (t *tengineCollector) Name() string { return "tengine" }

func (t *tengineCollector) Configure(decode func(v any) error) error {
	if err := decode(&t.cfg); err != nil {
		return err
	}
	if t.cfg.Timeout <= 0 {
		t.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (t *tengineCollector) Init(reg *registry.Registry) error {
	if t.cfg.Timeout <= 0 {
		if err := t.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	t.client = &http.Client{Timeout: t.cfg.Timeout}
	base := strings.TrimRight(t.cfg.URL, "/")
	if base == "" {
		base = "http://127.0.0.1/us"
	}
	t.url = base
	if _, err := t.status(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "tengine.bandwidth_total", Title: "Bandwidth", Units: "B/s", Type: registry.Area, Priority: 58600,
			Dimensions: []*registry.Dimension{{ID: "in", Algorithm: inc}, {ID: "out", Algorithm: inc, Multiplier: -1}}},
		{ID: "tengine.connections_total", Title: "Connections", Units: "connections/s", Priority: 58610,
			Dimensions: []*registry.Dimension{{ID: "accepted", Algorithm: inc}}},
		{ID: "tengine.requests_total", Title: "Requests", Units: "requests/s", Priority: 58620,
			Dimensions: []*registry.Dimension{{ID: "processed", Algorithm: inc}}},
		{ID: "tengine.requests_per_response_code_family_total", Title: "Requests Per Response Code Family", Units: "requests/s", Type: registry.Stacked, Priority: 58630,
			Dimensions: []*registry.Dimension{
				{ID: "2xx", Algorithm: inc}, {ID: "5xx", Algorithm: inc}, {ID: "3xx", Algorithm: inc},
				{ID: "4xx", Algorithm: inc}, {ID: "other", Algorithm: inc},
			}},
		{ID: "tengine.requests_upstream_total", Title: "Number Of Requests Calling For Upstream", Units: "requests/s", Priority: 58640,
			Dimensions: []*registry.Dimension{{ID: "requests", Algorithm: inc}}},
		{ID: "tengine.requests_upstream_per_response_code_family_total", Title: "Upstream Requests Per Response Code Family", Units: "requests/s", Type: registry.Stacked, Priority: 58650,
			Dimensions: []*registry.Dimension{{ID: "4xx", Algorithm: inc}, {ID: "5xx", Algorithm: inc}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "tengine", "tengine", "tengine"
		reg.AddChart(ch)
	}
	return nil
}

func (t *tengineCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	st, err := t.status(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("tengine.bandwidth_total", now, map[string]float64{"in": st["bytes_in"], "out": st["bytes_out"]})
	_ = reg.Collect("tengine.connections_total", now, map[string]float64{"accepted": st["conn_total"]})
	_ = reg.Collect("tengine.requests_total", now, map[string]float64{"processed": st["req_total"]})
	_ = reg.Collect("tengine.requests_per_response_code_family_total", now, map[string]float64{
		"2xx": st["http_2xx"], "5xx": st["http_5xx"], "3xx": st["http_3xx"], "4xx": st["http_4xx"], "other": st["http_other_status"],
	})
	_ = reg.Collect("tengine.requests_upstream_total", now, map[string]float64{"requests": st["ups_req"]})
	_ = reg.Collect("tengine.requests_upstream_per_response_code_family_total", now, map[string]float64{"4xx": st["http_ups_4xx"], "5xx": st["http_ups_5xx"]})
	return nil
}

var tengineKeys = []string{
	"bytes_in", "bytes_out", "conn_total", "req_total", "http_2xx", "http_3xx", "http_4xx", "http_5xx", "http_other_status",
	"rt", "ups_req", "ups_rt", "ups_tries", "http_200", "http_206", "http_302", "http_304", "http_403", "http_404",
	"http_416", "http_499", "http_500", "http_502", "http_503", "http_504", "http_508", "http_other_detail_status",
	"http_ups_4xx", "http_ups_5xx",
}

func (t *tengineCollector) status(ctx context.Context) (map[string]float64, error) {
	b, err := httpGet(ctx, t.client, t.url)
	if err != nil {
		return nil, fmt.Errorf("tengine: %w", err)
	}
	out := map[string]float64{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		i := tengineFirstInt(parts)
		if i < 0 || len(parts[i:]) < len(tengineKeys) {
			continue
		}
		nums := parts[i:]
		for k, key := range tengineKeys {
			n, _ := strconv.ParseFloat(nums[k], 64)
			out[key] += n
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("tengine: no reqstat")
	}
	return out, nil
}

func tengineFirstInt(parts []string) int {
	for i, p := range parts {
		if _, err := strconv.ParseInt(p, 10, 64); err == nil {
			return i
		}
	}
	return -1
}
