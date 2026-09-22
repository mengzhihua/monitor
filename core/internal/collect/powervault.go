package collect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// powervaultConfig is collectors.modules.powervault (ME4/ME5 MCI REST).
type powervaultConfig struct {
	TLS      CollectorTLS  `yaml:"tls"`
	URL      string        `yaml:"url"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type powervaultCollector struct {
	cfg    powervaultConfig
	client *http.Client
	url    string
	get    func(ctx context.Context, path string) ([]byte, error)
}

func init() {
	Register("powervault", func() Collector { return &powervaultCollector{} })
}

func (p *powervaultCollector) Name() string { return "powervault" }

func (p *powervaultCollector) Configure(decode func(v any) error) error {
	if err := decode(&p.cfg); err != nil {
		return err
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (p *powervaultCollector) Init(reg *registry.Registry) error {
	if p.cfg.Timeout <= 0 {
		if err := p.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	client, tlsErr := collectorHTTPClient(p.cfg.Timeout, p.cfg.TLS)
	if tlsErr != nil {
		return tlsErr
	}
	p.client = client
	base := strings.TrimRight(p.cfg.URL, "/")
	if base == "" {
		base = "https://127.0.0.1"
	}
	p.url = base
	if _, err := p.system(context.Background()); err != nil {
		return err
	}
	for _, ch := range []*registry.Chart{
		{ID: "powervault.system_health", Title: "System health", Units: "status", Priority: 61600,
			Dimensions: []*registry.Dimension{{ID: "ok"}}},
		{ID: "powervault.sensor_status", Title: "Sensor status", Units: "sensors", Type: registry.Stacked, Priority: 61610,
			Dimensions: []*registry.Dimension{{ID: "ok"}, {ID: "error"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "powervault", "powervault", "powervault"
		reg.AddChart(ch)
	}
	return nil
}

func (p *powervaultCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	st, err := p.system(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("powervault.system_health", now, map[string]float64{"ok": st["ok"]})
	_ = reg.Collect("powervault.sensor_status", now, map[string]float64{"ok": st["sensor_ok"], "error": st["sensor_error"]})
	return nil
}

func (p *powervaultCollector) system(ctx context.Context) (map[string]float64, error) {
	body, err := p.fetch(ctx, "/api/show/system")
	if err != nil {
		return nil, err
	}
	s := string(body)
	out := map[string]float64{"ok": 0, "sensor_ok": 0, "sensor_error": 0}
	low := strings.ToLower(s)
	if strings.Contains(low, `"health":"ok"`) || strings.Contains(low, `"health": "ok"`) || strings.Contains(low, "health-numeric\":0") || strings.Contains(low, `"system-health":"ok"`) {
		out["ok"] = 1
	}
	if nest := firstJSONObject(body); nest != nil {
		h := strings.ToLower(nestString(nest, "health") + nestString(nest, "system-health") + nestString(nest, "health-reason"))
		if h == "ok" || h == "good" || nestFloat(nest, "health-numeric") == 0 && (nestString(nest, "health") != "" || nestString(nest, "system-health") != "") {
			if nestString(nest, "health") == "OK" || nestString(nest, "health") == "ok" || nestString(nest, "system-health") == "OK" {
				out["ok"] = 1
			}
		}
		if nestFloat(nest, "ok") == 1 {
			out["ok"] = 1
		}
	}
	sb, err := p.fetch(ctx, "/api/show/sensor-status")
	if err == nil {
		ok, bad := countJSONHealth(sb)
		out["sensor_ok"], out["sensor_error"] = ok, bad
	}
	if out["ok"] == 0 && !strings.Contains(low, "health") && nestFloat(firstJSONObject(body), "ok") == 0 && !strings.Contains(s, "system") {
		return nil, fmt.Errorf("powervault: unexpected response")
	}
	return out, nil
}

func (p *powervaultCollector) fetch(ctx context.Context, path string) ([]byte, error) {
	if p.get != nil {
		return p.get(ctx, path)
	}
	user := p.cfg.User
	if user == "" {
		user = "manage"
	}
	sum := sha256.Sum256([]byte(user + "_" + p.cfg.Password))
	hash := hex.EncodeToString(sum[:])
	login, err := httpGet(ctx, p.client, p.url+"/api/login/"+hash)
	if err != nil {
		return nil, fmt.Errorf("powervault: %w", err)
	}
	key := strings.Trim(strings.TrimSpace(string(login)), `"`)
	if m, err := jsonMap(login); err == nil {
		if sl := nestSlice(m, "status"); len(sl) > 0 {
			if mm, ok := sl[0].(map[string]any); ok {
				if r := nestString(mm, "response"); r != "" {
					key = r
				}
			}
		}
		if nestString(m, "sessionKey") != "" {
			key = nestString(m, "sessionKey")
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("sessionKey", key)
	req.Header.Set("dataType", "json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("powervault: %w", err)
	}
	defer resp.Body.Close()
	b, err := readBody(resp)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("powervault: %s", resp.Status)
	}
	return b, nil
}

func firstJSONObject(b []byte) map[string]any {
	if m, err := jsonMap(b); err == nil {
		if inner := nestMap(m, "system", "0"); inner != nil {
			return inner
		}
		if sl := nestSlice(m, "system"); len(sl) > 0 {
			if mm, ok := sl[0].(map[string]any); ok {
				return mm
			}
		}
		return m
	}
	return nil
}

func countJSONHealth(b []byte) (ok, bad float64) {
	s := strings.ToLower(string(b))
	ok = float64(strings.Count(s, `"status":"ok"`) + strings.Count(s, `"status": "ok"`) + strings.Count(s, `"health":"ok"`))
	bad = float64(strings.Count(s, `"status":"error"`) + strings.Count(s, `"health":"error"`) + strings.Count(s, `"status":"fault"`))
	return
}
