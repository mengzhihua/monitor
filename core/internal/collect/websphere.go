package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// websphereConfig is collectors.modules.websphere (ibm.d PMI JSON / Prometheus).
type websphereConfig struct {
	URL      string        `yaml:"url"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type websphereCollector struct {
	cfg    websphereConfig
	client *http.Client
	get    func(ctx context.Context) ([]byte, error)
}

func init() {
	Register("websphere", func() Collector { return &websphereCollector{} })
}

func (w *websphereCollector) Name() string { return "websphere" }

func (w *websphereCollector) Configure(decode func(v any) error) error {
	if err := decode(&w.cfg); err != nil {
		return err
	}
	if w.cfg.URL == "" {
		w.cfg.URL = "http://127.0.0.1:9080/metrics"
	}
	if w.cfg.Timeout <= 0 {
		w.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (w *websphereCollector) Init(reg *registry.Registry) error {
	if w.cfg.URL == "" {
		if err := w.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if w.get == nil {
		w.client = &http.Client{Timeout: w.cfg.Timeout}
		w.get = w.httpGet
	}
	if _, err := w.sample(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "websphere.jvm_heap", Context: "websphere.jvm_heap", Title: "WebSphere JVM heap", Units: "MiB", Family: "jvm", Type: registry.Stacked, Priority: 64400,
			Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "free"}}},
		{ID: "websphere.thread_pool", Context: "websphere.thread_pool", Title: "WebSphere thread pool", Units: "threads", Family: "threads", Priority: 64410,
			Dimensions: []*registry.Dimension{{ID: "active"}, {ID: "pool"}}},
		{ID: "websphere.sessions", Context: "websphere.sessions", Title: "WebSphere HTTP sessions", Units: "sessions", Family: "sessions", Priority: 64420,
			Dimensions: []*registry.Dimension{{ID: "live"}}},
		{ID: "websphere.servlet_requests", Context: "websphere.servlet_requests", Title: "WebSphere servlet requests", Units: "requests/s", Family: "servlet", Priority: 64430,
			Dimensions: []*registry.Dimension{{ID: "requests", Algorithm: inc}}},
	} {
		ch.Plugin, ch.Module = "ibm.d", "websphere"
		reg.AddChart(ch)
	}
	return nil
}

func (w *websphereCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	s, err := w.sample(ctx)
	if err != nil {
		return err
	}
	used, max := s["used"], s["max"]
	free := max - used
	if free < 0 {
		free = 0
	}
	_ = reg.Collect("websphere.jvm_heap", now, map[string]float64{"used": used, "free": free})
	_ = reg.Collect("websphere.thread_pool", now, map[string]float64{"active": s["active"], "pool": s["pool"]})
	_ = reg.Collect("websphere.sessions", now, map[string]float64{"live": s["live"]})
	_ = reg.Collect("websphere.servlet_requests", now, map[string]float64{"requests": s["requests"]})
	return nil
}

func (w *websphereCollector) httpGet(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.cfg.URL, nil)
	if err != nil {
		return nil, err
	}
	if w.cfg.User != "" {
		req.SetBasicAuth(w.cfg.User, w.cfg.Password)
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("websphere: %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 2<<20))
}

func (w *websphereCollector) sample(ctx context.Context) (map[string]float64, error) {
	b, err := w.get(ctx)
	if err != nil {
		return nil, fmt.Errorf("websphere: %w", err)
	}
	out := parseWebsphere(b)
	if len(out) == 0 {
		return nil, fmt.Errorf("websphere: no metrics")
	}
	return out, nil
}

func parseWebsphere(b []byte) map[string]float64 {
	s := strings.TrimSpace(string(b))
	if strings.HasPrefix(s, "{") {
		var raw map[string]any
		if err := json.Unmarshal(b, &raw); err == nil {
			out := map[string]float64{}
			flattenNum(raw, "", out)
			return mapWebsphereKeys(out)
		}
	}
	out := map[string]float64{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			continue
		}
		name := fields[0]
		if i := strings.IndexByte(name, '{'); i > 0 {
			name = name[:i]
		}
		out[name] = v
	}
	return mapWebsphereKeys(out)
}

func flattenNum(v any, prefix string, out map[string]float64) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			flattenNum(child, key, out)
		}
	case float64:
		out[strings.ToLower(prefix)] = t
	case json.Number:
		f, _ := t.Float64()
		out[strings.ToLower(prefix)] = f
	}
}

func mapWebsphereKeys(in map[string]float64) map[string]float64 {
	out := map[string]float64{}
	pick := func(dst string, keys ...string) {
		for _, k := range keys {
			k = strings.ToLower(k)
			if v, ok := in[k]; ok {
				out[dst] = v
				return
			}
			for ik, v := range in {
				if strings.Contains(ik, k) {
					out[dst] = v
					return
				}
			}
		}
	}
	pick("used", "used", "heap_used", "heapused", "usedheap", "jvm.heap.used", "base_memory_usedheap_bytes")
	pick("max", "max", "heap_max", "heapmax", "maxheap", "jvm.heap.max")
	pick("active", "active", "threads_active", "thread.active", "activedcount")
	pick("pool", "pool", "threads_pool", "poolsize", "thread.pool")
	pick("live", "live", "sessions", "httpsessions", "livecount")
	pick("requests", "requests", "servlet_requests", "requestcount", "totalservletrequests")
	if len(out) == 0 {
		return in
	}
	return out
}
