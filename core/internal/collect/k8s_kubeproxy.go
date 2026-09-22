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

// k8sKubeproxyConfig is collectors.modules.k8s_kubeproxy.
type k8sKubeproxyConfig struct {
	TLS     CollectorTLS  `yaml:"tls"`
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type k8sKubeproxyCollector struct {
	cfg    k8sKubeproxyConfig
	client *http.Client
	url    string
}

func init() {
	Register("k8s_kubeproxy", func() Collector { return &k8sKubeproxyCollector{} })
}

func (k *k8sKubeproxyCollector) Name() string { return "k8s_kubeproxy" }

func (k *k8sKubeproxyCollector) Configure(decode func(v any) error) error {
	if err := decode(&k.cfg); err != nil {
		return err
	}
	if k.cfg.Timeout <= 0 {
		k.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (k *k8sKubeproxyCollector) Init(reg *registry.Registry) error {
	if k.cfg.Timeout <= 0 {
		if err := k.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	client, tlsErr := collectorHTTPClient(k.cfg.Timeout, k.cfg.TLS)
	if tlsErr != nil {
		return tlsErr
	}
	k.client = client
	urls := []string{k.cfg.URL}
	if k.cfg.URL == "" {
		urls = []string{"http://127.0.0.1:10249/metrics"}
	}
	u, body, err := httpGetTry(context.Background(), k.client, urls)
	if err != nil {
		return err
	}
	if !strings.Contains(string(body), "kubeproxy_") && !strings.Contains(string(body), "kube_proxy") {
		return fmt.Errorf("k8s_kubeproxy: no kubeproxy metrics")
	}
	k.url = u
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "k8s_kubeproxy.kubeproxy_sync_proxy_rules", Title: "Sync Proxy Rules", Units: "events/s", Priority: 54100,
			Dimensions: []*registry.Dimension{{ID: "sync_proxy_rules", Algorithm: inc}}},
		{ID: "k8s_kubeproxy.rest_client_requests_by_code", Title: "HTTP Requests By Status Code", Units: "requests/s", Type: registry.Stacked, Priority: 54110},
		{ID: "k8s_kubeproxy.rest_client_requests_by_method", Title: "HTTP Requests By Status Method", Units: "requests/s", Type: registry.Stacked, Priority: 54120},
		{ID: "k8s_kubeproxy.http_request_duration", Title: "HTTP Requests Duration", Units: "microseconds", Priority: 54130,
			Dimensions: []*registry.Dimension{{ID: "0.5"}, {ID: "0.9"}, {ID: "0.99"}}},
	} {
		c.Family, c.Plugin, c.Module = "k8s_kubeproxy", "k8s_kubeproxy", "k8s_kubeproxy"
		reg.AddChart(c)
	}
	return nil
}

func (k *k8sKubeproxyCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	b, err := httpGet(ctx, k.client, k.url)
	if err != nil {
		return err
	}
	samples := promSamples(string(b))
	_ = reg.Collect("k8s_kubeproxy.kubeproxy_sync_proxy_rules", now, map[string]float64{
		"sync_proxy_rules": promSumPrefixed(samples,
			"kubeproxy_sync_proxy_rules_duration_seconds_count",
			"sync_proxy_rules_count"),
	})
	codes := promSumByLabel(samples, "rest_client_requests_total", "code")
	k.labeled(reg, now, "k8s_kubeproxy.rest_client_requests_by_code", codes)
	methods := promSumByLabel(samples, "rest_client_requests_total", "method")
	k.labeled(reg, now, "k8s_kubeproxy.rest_client_requests_by_method", methods)
	_ = reg.Collect("k8s_kubeproxy.http_request_duration", now, map[string]float64{
		"0.5":  kubeproxyQuantile(samples, "rest_client_request_duration_seconds", 0.5) * 1e6,
		"0.9":  kubeproxyQuantile(samples, "rest_client_request_duration_seconds", 0.9) * 1e6,
		"0.99": kubeproxyQuantile(samples, "rest_client_request_duration_seconds", 0.99) * 1e6,
	})
	return nil
}

func (k *k8sKubeproxyCollector) labeled(reg *registry.Registry, now time.Time, chart string, vals map[string]float64) {
	out := map[string]float64{}
	for name, v := range vals {
		id := sanitizeID(name)
		if id == "" {
			continue
		}
		ensureDim(reg, chart, id, &registry.Dimension{ID: id, Name: name, Algorithm: registry.Incremental})
		out[id] = v
	}
	if len(out) > 0 {
		_ = reg.Collect(chart, now, out)
	}
}

func kubeproxyQuantile(samples []ingest.Sample, name string, q float64) float64 {
	want := fmt.Sprintf("%g", q)
	for _, s := range samples {
		if s.Name != name {
			continue
		}
		if promLabel(s, "quantile") == want {
			return s.Value
		}
	}
	return 0
}
