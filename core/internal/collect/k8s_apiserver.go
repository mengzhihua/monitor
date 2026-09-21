package collect

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// k8sApiserverConfig is collectors.modules.k8s_apiserver.
type k8sApiserverConfig struct {
	URL     string        `yaml:"url"`
	Token   string        `yaml:"token"`
	Timeout time.Duration `yaml:"timeout"`
}

type k8sApiserverCollector struct {
	cfg    k8sApiserverConfig
	client *http.Client
	url    string
	token  string
}

func init() {
	Register("k8s_apiserver", func() Collector { return &k8sApiserverCollector{} })
}

func (k *k8sApiserverCollector) Name() string { return "k8s_apiserver" }

func (k *k8sApiserverCollector) Configure(decode func(v any) error) error {
	if err := decode(&k.cfg); err != nil {
		return err
	}
	if k.cfg.Timeout <= 0 {
		k.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (k *k8sApiserverCollector) Init(reg *registry.Registry) error {
	if k.cfg.Timeout <= 0 {
		if err := k.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	k.client = insecureClient(k.cfg.Timeout)
	k.token = k8sBearer(k.cfg.Token)
	urls := []string{k.cfg.URL}
	if k.cfg.URL == "" {
		urls = k8sDefaultURLs("/metrics")
	}
	u, body, err := httpGetTokenTry(context.Background(), k.client, urls, k.token)
	if err != nil {
		return err
	}
	if !strings.Contains(string(body), "apiserver_") {
		return fmt.Errorf("k8s_apiserver: no apiserver_ metrics")
	}
	k.url = u
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "k8s_apiserver.requests_total", Title: "API Server Request Rate", Units: "requests/s", Priority: 54200,
			Dimensions: []*registry.Dimension{{ID: "requests", Algorithm: inc}}},
		{ID: "k8s_apiserver.requests_dropped", Title: "API Server Dropped Requests", Units: "requests/s", Priority: 54210,
			Dimensions: []*registry.Dimension{{ID: "dropped", Algorithm: inc}}},
		{ID: "k8s_apiserver.requests_by_code", Title: "API Server Requests By Status Code", Units: "requests/s", Type: registry.Stacked, Priority: 54220},
		{ID: "k8s_apiserver.requests_by_verb", Title: "API Server Requests By Verb", Units: "requests/s", Type: registry.Stacked, Priority: 54230},
		{ID: "k8s_apiserver.inflight_requests", Title: "API Server Inflight Requests", Units: "requests", Type: registry.Stacked, Priority: 54240,
			Dimensions: []*registry.Dimension{{ID: "mutating"}, {ID: "read_only"}}},
	} {
		c.Family, c.Plugin, c.Module = "k8s_apiserver", "k8s_apiserver", "k8s_apiserver"
		reg.AddChart(c)
	}
	return nil
}

func (k *k8sApiserverCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	b, err := httpGetToken(ctx, k.client, k.url, k.token)
	if err != nil {
		return err
	}
	samples := promSamples(string(b))
	_ = reg.Collect("k8s_apiserver.requests_total", now, map[string]float64{
		"requests": promSum(samples, "apiserver_request_total")})
	_ = reg.Collect("k8s_apiserver.requests_dropped", now, map[string]float64{
		"dropped": promSum(samples, "apiserver_dropped_requests_total")})
	codes := promSumByLabel(samples, "apiserver_request_total", "code")
	var five float64
	for name, v := range codes {
		if strings.HasPrefix(name, "5") {
			five += v
		}
	}
	if five != 0 || len(codes) > 0 {
		codes["5xx"] = five
	}
	k.labeled(reg, now, "k8s_apiserver.requests_by_code", codes)
	k.labeled(reg, now, "k8s_apiserver.requests_by_verb", promSumByLabel(samples, "apiserver_request_total", "verb"))
	inflight := promSumByLabel(samples, "apiserver_current_inflight_requests", "requestKind")
	readOnly := inflight["readOnly"]
	if readOnly == 0 {
		readOnly = inflight["readonly"]
	}
	_ = reg.Collect("k8s_apiserver.inflight_requests", now, map[string]float64{
		"mutating": inflight["mutating"], "read_only": readOnly})
	return nil
}

func (k *k8sApiserverCollector) labeled(reg *registry.Registry, now time.Time, chart string, vals map[string]float64) {
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

func k8sBearer(explicit string) string {
	if explicit != "" {
		return explicit
	}
	b, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func k8sDefaultURLs(path string) []string {
	if path != "" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	out := []string{}
	if h := os.Getenv("KUBERNETES_SERVICE_HOST"); h != "" {
		p := os.Getenv("KUBERNETES_SERVICE_PORT")
		if p == "" {
			p = "443"
		}
		out = append(out, "https://"+h+":"+p+path)
	}
	out = append(out, "https://127.0.0.1:6443"+path, "http://127.0.0.1:8080"+path)
	return out
}
