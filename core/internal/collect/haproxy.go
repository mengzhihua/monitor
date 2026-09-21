package collect

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// haproxyConfig is collectors.modules.haproxy (stats CSV, Netdata go.d haproxy).
type haproxyConfig struct {
	URL      string        `yaml:"url"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type haproxyCollector struct {
	cfg    haproxyConfig
	client *http.Client
}

func init() {
	Register("haproxy", func() Collector { return &haproxyCollector{} })
}

func (h *haproxyCollector) Name() string { return "haproxy" }

func (h *haproxyCollector) Configure(decode func(v any) error) error {
	if err := decode(&h.cfg); err != nil {
		return err
	}
	if h.cfg.Timeout <= 0 {
		h.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (h *haproxyCollector) Init(reg *registry.Registry) error {
	if h.cfg.Timeout <= 0 {
		if err := h.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	h.client = &http.Client{Timeout: h.cfg.Timeout}
	urls := []string{h.cfg.URL}
	if h.cfg.URL == "" {
		urls = []string{
			"http://127.0.0.1:8404/;csv",
			"http://127.0.0.1:8404/stats;csv",
			"http://127.0.0.1/haproxy?stats;csv",
		}
	}
	var last error
	for _, u := range urls {
		h.cfg.URL = u
		if _, err := h.fetch(context.Background()); err == nil {
			last = nil
			break
		} else {
			last = err
		}
	}
	if last != nil {
		return last
	}
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "haproxy.frontend_current_sessions", Title: "HAProxy frontend sessions", Units: "sessions", Priority: 43000,
			Dimensions: []*registry.Dimension{{ID: "current"}, {ID: "max"}}},
		{ID: "haproxy.frontend_bytes", Title: "HAProxy frontend traffic", Units: "bytes/s", Type: registry.Area, Priority: 43010,
			Dimensions: []*registry.Dimension{{ID: "in", Algorithm: inc}, {ID: "out", Algorithm: inc, Multiplier: -1}}},
		{ID: "haproxy.frontend_responses", Title: "HAProxy frontend responses", Units: "responses/s", Type: registry.Stacked, Priority: 43020,
			Dimensions: []*registry.Dimension{
				{ID: "1xx", Algorithm: inc}, {ID: "2xx", Algorithm: inc}, {ID: "3xx", Algorithm: inc},
				{ID: "4xx", Algorithm: inc}, {ID: "5xx", Algorithm: inc}, {ID: "other", Algorithm: inc}}},
		{ID: "haproxy.backend_current_queue", Title: "HAProxy backend queue", Units: "requests", Priority: 43030,
			Dimensions: []*registry.Dimension{{ID: "queue"}}},
		{ID: "haproxy.backend_servers", Title: "HAProxy backend servers", Units: "servers", Type: registry.Stacked, Priority: 43040,
			Dimensions: []*registry.Dimension{{ID: "active"}, {ID: "backup"}}},
		{ID: "haproxy.backend_status", Title: "HAProxy backend status", Units: "backends", Type: registry.Stacked, Priority: 43050,
			Dimensions: []*registry.Dimension{{ID: "up"}, {ID: "down"}}},
	} {
		c.Family, c.Plugin, c.Module = "haproxy", "haproxy", "haproxy"
		reg.AddChart(c)
	}
	return nil
}

func (h *haproxyCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	rows, err := h.fetch(ctx)
	if err != nil {
		return err
	}
	var fcur, fmax, fbin, fbout, r1, r2, r3, r4, r5, roth float64
	var bq, bact, bbck, bup, bdown float64
	for _, r := range rows {
		switch r["type"] {
		case "0": // frontend
			fcur += r.f("scur")
			fmax += r.f("smax")
			fbin += r.f("bin")
			fbout += r.f("bout")
			r1 += r.f("hrsp_1xx")
			r2 += r.f("hrsp_2xx")
			r3 += r.f("hrsp_3xx")
			r4 += r.f("hrsp_4xx")
			r5 += r.f("hrsp_5xx")
			roth += r.f("hrsp_other")
		case "1": // backend
			bq += r.f("qcur")
			bact += r.f("act")
			bbck += r.f("bck")
			if strings.EqualFold(r["status"], "UP") {
				bup++
			} else if r["status"] != "" && r["status"] != "OPEN" {
				bdown++
			}
		}
	}
	_ = reg.Collect("haproxy.frontend_current_sessions", now, map[string]float64{"current": fcur, "max": fmax})
	_ = reg.Collect("haproxy.frontend_bytes", now, map[string]float64{"in": fbin, "out": fbout})
	_ = reg.Collect("haproxy.frontend_responses", now, map[string]float64{"1xx": r1, "2xx": r2, "3xx": r3, "4xx": r4, "5xx": r5, "other": roth})
	_ = reg.Collect("haproxy.backend_current_queue", now, map[string]float64{"queue": bq})
	_ = reg.Collect("haproxy.backend_servers", now, map[string]float64{"active": bact, "backup": bbck})
	_ = reg.Collect("haproxy.backend_status", now, map[string]float64{"up": bup, "down": bdown})
	return nil
}

type haproxyRow map[string]string

func (r haproxyRow) f(k string) float64 {
	v, _ := strconv.ParseFloat(r[k], 64)
	return v
}

func (h *haproxyCollector) fetch(ctx context.Context) ([]haproxyRow, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.cfg.URL, nil)
	if err != nil {
		return nil, err
	}
	if h.cfg.User != "" {
		req.SetBasicAuth(h.cfg.User, h.cfg.Password)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("haproxy: %s -> %s", h.cfg.URL, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	return parseHAProxyCSV(string(body))
}

func parseHAProxyCSV(s string) ([]haproxyRow, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "# ")
	s = strings.TrimPrefix(s, "#")
	r := csv.NewReader(strings.NewReader(s))
	r.TrimLeadingSpace = true
	r.FieldsPerRecord = -1
	recs, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("haproxy csv: %w", err)
	}
	if len(recs) < 2 {
		return nil, fmt.Errorf("haproxy: empty stats csv")
	}
	headers := recs[0]
	for i, h := range headers {
		headers[i] = strings.TrimSpace(strings.TrimPrefix(h, "#"))
	}
	var out []haproxyRow
	for _, rec := range recs[1:] {
		row := haproxyRow{}
		for i := 0; i < len(headers) && i < len(rec); i++ {
			row[headers[i]] = rec[i]
		}
		if row["pxname"] == "" {
			continue
		}
		out = append(out, row)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("haproxy: no proxy rows")
	}
	return out, nil
}
