package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/ingest"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

// dcgmConfig is collectors.modules.dcgm (dcgm-exporter Prometheus).
type dcgmConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type dcgmCollector struct {
	cfg    dcgmConfig
	client *http.Client
	url    string
}

func init() {
	Register("dcgm", func() Collector { return &dcgmCollector{} })
}

func (d *dcgmCollector) Name() string { return "dcgm" }

func (d *dcgmCollector) Configure(decode func(v any) error) error {
	if err := decode(&d.cfg); err != nil {
		return err
	}
	if d.cfg.Timeout <= 0 {
		d.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (d *dcgmCollector) Init(reg *registry.Registry) error {
	if d.cfg.Timeout <= 0 {
		if err := d.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	d.client = &http.Client{Timeout: d.cfg.Timeout}
	urls := []string{d.cfg.URL}
	if d.cfg.URL == "" {
		urls = []string{"http://127.0.0.1:9400/metrics", "http://127.0.0.1:9400/"}
	}
	u, body, err := httpGetTry(context.Background(), d.client, urls)
	if err != nil {
		return err
	}
	d.url = u
	if !strings.Contains(string(body), "DCGM_FI_") {
		return fmt.Errorf("dcgm: not dcgm-exporter metrics")
	}
	for _, ch := range []*registry.Chart{
		{ID: "dcgm.gpu_utilization", Title: "GPU utilization", Units: "%", Priority: 61300},
		{ID: "dcgm.fb_used", Title: "Framebuffer used", Units: "MiB", Priority: 61310},
		{ID: "dcgm.gpu_temp", Title: "GPU temperature", Units: "Celsius", Priority: 61320},
		{ID: "dcgm.power_usage", Title: "GPU power", Units: "Watts", Priority: 61330},
	} {
		ch.Family, ch.Plugin, ch.Module = "dcgm", "dcgm", "dcgm"
		reg.AddChart(ch)
	}
	return nil
}

func (d *dcgmCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	b, err := httpGet(ctx, d.client, d.url)
	if err != nil {
		return fmt.Errorf("dcgm: %w", err)
	}
	samples := promSamples(string(b))
	d.collectLabeled(reg, now, samples, "DCGM_FI_DEV_GPU_UTIL", "dcgm.gpu_utilization")
	d.collectLabeled(reg, now, samples, "DCGM_FI_DEV_FB_USED", "dcgm.fb_used")
	d.collectLabeled(reg, now, samples, "DCGM_FI_DEV_GPU_TEMP", "dcgm.gpu_temp")
	d.collectLabeled(reg, now, samples, "DCGM_FI_DEV_POWER_USAGE", "dcgm.power_usage")
	return nil
}

func (d *dcgmCollector) collectLabeled(reg *registry.Registry, now time.Time, samples []ingest.Sample, metric, chart string) {
	vals := promSumByLabel(samples, metric, "gpu")
	if len(vals) == 0 {
		vals = map[string]float64{"gpu": promSum(samples, metric)}
	}
	for id := range vals {
		ensureDim(reg, chart, id, &registry.Dimension{ID: id})
	}
	_ = reg.Collect(chart, now, vals)
}
