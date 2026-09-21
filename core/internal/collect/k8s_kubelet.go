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

// k8sKubeletConfig is collectors.modules.k8s_kubelet.
type k8sKubeletConfig struct {
	URL     string        `yaml:"url"`
	Token   string        `yaml:"token"`
	Timeout time.Duration `yaml:"timeout"`
}

type k8sKubeletCollector struct {
	cfg    k8sKubeletConfig
	client *http.Client
	url    string
}

func init() {
	Register("k8s_kubelet", func() Collector { return &k8sKubeletCollector{} })
}

func (k *k8sKubeletCollector) Name() string { return "k8s_kubelet" }

func (k *k8sKubeletCollector) Configure(decode func(v any) error) error {
	if err := decode(&k.cfg); err != nil {
		return err
	}
	if k.cfg.Timeout <= 0 {
		k.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (k *k8sKubeletCollector) Init(reg *registry.Registry) error {
	if k.cfg.Timeout <= 0 {
		if err := k.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	k.client = insecureClient(k.cfg.Timeout)
	urls := []string{k.cfg.URL}
	if k.cfg.URL == "" {
		urls = []string{
			"http://127.0.0.1:10255/metrics",
			"https://127.0.0.1:10250/metrics",
			"http://127.0.0.1:10250/metrics",
		}
	}
	u, body, err := httpGetTokenTry(context.Background(), k.client, urls, k.cfg.Token)
	if err != nil {
		return err
	}
	if !strings.Contains(string(body), "kubelet_") {
		return fmt.Errorf("k8s_kubelet: no kubelet_ metrics")
	}
	k.url = u
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "k8s_kubelet.kubelet_pods_running", Title: "Number Of Pods Currently Running", Units: "running_pods", Priority: 54000,
			Dimensions: []*registry.Dimension{{ID: "total"}}},
		{ID: "k8s_kubelet.kubelet_containers_running", Title: "Number Of Containers Currently Running", Units: "running_containers", Priority: 54010,
			Dimensions: []*registry.Dimension{{ID: "total"}}},
		{ID: "k8s_kubelet.kubelet_runtime_operations", Title: "Runtime Operations By Type", Units: "operations/s", Type: registry.Stacked, Priority: 54020},
		{ID: "k8s_kubelet.kubelet_runtime_operations_errors", Title: "Runtime Operations Errors By Type", Units: "errors/s", Type: registry.Stacked, Priority: 54030},
		{ID: "k8s_kubelet.kubelet_node_config_error", Title: "Node Configuration-Related Error", Units: "bool", Priority: 54040,
			Dimensions: []*registry.Dimension{{ID: "experiencing_error"}}},
		{ID: "k8s_kubelet.rest_client_requests_by_code", Title: "HTTP Requests By Status Code", Units: "requests/s", Type: registry.Stacked, Priority: 54050},
		{ID: "k8s_kubelet.rest_client_requests_by_method", Title: "HTTP Requests By Status Method", Units: "requests/s", Type: registry.Stacked, Priority: 54060},
		{ID: "k8s_kubelet.kubelet_token_requests", Title: "Token() Requests", Units: "token_requests/s", Priority: 54070,
			Dimensions: []*registry.Dimension{{ID: "total", Algorithm: inc}, {ID: "failed", Algorithm: inc}}},
	} {
		c.Family, c.Plugin, c.Module = "k8s_kubelet", "k8s_kubelet", "k8s_kubelet"
		reg.AddChart(c)
	}
	return nil
}

func (k *k8sKubeletCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	b, err := httpGetToken(ctx, k.client, k.url, k.cfg.Token)
	if err != nil {
		return err
	}
	samples := promSamples(string(b))
	_ = reg.Collect("k8s_kubelet.kubelet_pods_running", now, map[string]float64{
		"total": promSumPrefixed(samples, "kubelet_running_pods", "kubelet_running_pod_count")})
	_ = reg.Collect("k8s_kubelet.kubelet_containers_running", now, map[string]float64{
		"total": promSumPrefixed(samples, "kubelet_running_containers", "kubelet_running_container_count")})
	ops := kubeletByOp(samples, "kubelet_runtime_operations_total")
	k.collectLabeled(reg, now, "k8s_kubelet.kubelet_runtime_operations", ops, registry.Incremental)
	errs := kubeletByOp(samples, "kubelet_runtime_operations_errors_total")
	k.collectLabeled(reg, now, "k8s_kubelet.kubelet_runtime_operations_errors", errs, registry.Incremental)
	_ = reg.Collect("k8s_kubelet.kubelet_node_config_error", now, map[string]float64{
		"experiencing_error": promSum(samples, "kubelet_node_config_error")})
	codes := promSumByLabel(samples, "rest_client_requests_total", "code")
	k.collectLabeled(reg, now, "k8s_kubelet.rest_client_requests_by_code", codes, registry.Incremental)
	methods := promSumByLabel(samples, "rest_client_requests_total", "method")
	k.collectLabeled(reg, now, "k8s_kubelet.rest_client_requests_by_method", methods, registry.Incremental)
	_ = reg.Collect("k8s_kubelet.kubelet_token_requests", now, map[string]float64{
		"total":  promSumPrefixed(samples, "token_count", "kubelet_token_count"),
		"failed": promSumPrefixed(samples, "token_fail_count", "kubelet_token_fail_count"),
	})
	return nil
}

func (k *k8sKubeletCollector) collectLabeled(reg *registry.Registry, now time.Time, chart string, vals map[string]float64, algo registry.Algorithm) {
	out := map[string]float64{}
	for name, v := range vals {
		id := sanitizeID(name)
		if id == "" {
			id = "unknown"
		}
		ensureDim(reg, chart, id, &registry.Dimension{ID: id, Name: name, Algorithm: algo})
		out[id] = v
	}
	if len(out) > 0 {
		_ = reg.Collect(chart, now, out)
	}
}

func kubeletByOp(samples []ingest.Sample, name string) map[string]float64 {
	out := promSumByLabel(samples, name, "operation_type")
	if len(out) == 1 && out[""] != 0 || len(out) == 0 {
		if alt := promSumByLabel(samples, name, "operation"); len(alt) > 0 {
			return alt
		}
	}
	delete(out, "")
	return out
}
