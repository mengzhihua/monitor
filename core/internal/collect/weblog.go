package collect

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// weblogConfig is collectors.modules.weblog (Apache/Nginx access log).
type weblogConfig struct {
	Path    string        `yaml:"path"`
	Timeout time.Duration `yaml:"timeout"`
}

type weblogCollector struct {
	cfg    weblogConfig
	cursor *fileCursor
	totals weblogTotals
}

type weblogTotals struct {
	requests, unmatched, bytesSent float64
	c1, c2, c3, c4, c5             float64
	success, bad, redirect, err    float64
	get, post, other               float64
}

func init() {
	Register("weblog", func() Collector { return &weblogCollector{} })
}

func (w *weblogCollector) Name() string { return "weblog" }

func (w *weblogCollector) Configure(decode func(v any) error) error {
	if err := decode(&w.cfg); err != nil {
		return err
	}
	return nil
}

func (w *weblogCollector) Init(reg *registry.Registry) error {
	_ = w.Configure(func(any) error { return nil })
	path := w.cfg.Path
	if path == "" {
		for _, p := range []string{"/var/log/nginx/access.log", "/var/log/apache2/access.log", "/var/log/httpd/access_log"} {
			if _, err := os.Stat(p); err == nil {
				path = p
				break
			}
		}
	}
	if path == "" {
		return fmt.Errorf("weblog: no access log")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("weblog: %w", err)
	}
	w.cursor = &fileCursor{path: path}
	if err := w.cursor.skipToEnd(); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "web_log.requests", Title: "Web Log Requests", Units: "requests/s", Priority: 52400,
			Dimensions: []*registry.Dimension{{ID: "requests", Algorithm: inc}}},
		{ID: "web_log.type_requests", Title: "Web Log Requests By Type", Units: "requests/s", Type: registry.Stacked, Priority: 52410,
			Dimensions: []*registry.Dimension{
				{ID: "success", Algorithm: inc}, {ID: "bad", Algorithm: inc},
				{ID: "redirect", Algorithm: inc}, {ID: "error", Algorithm: inc}}},
		{ID: "web_log.status_code_class_responses", Title: "Responses By HTTP Status Code Class", Units: "responses/s", Type: registry.Stacked, Priority: 52420,
			Dimensions: []*registry.Dimension{
				{ID: "1xx", Algorithm: inc}, {ID: "2xx", Algorithm: inc}, {ID: "3xx", Algorithm: inc},
				{ID: "4xx", Algorithm: inc}, {ID: "5xx", Algorithm: inc}}},
		{ID: "web_log.bandwidth", Title: "Bandwidth", Units: "kilobits/s", Type: registry.Area, Priority: 52430,
			Dimensions: []*registry.Dimension{{ID: "sent", Algorithm: inc, Multiplier: 8, Divisor: 1000}}},
		{ID: "web_log.http_method_requests", Title: "Requests By HTTP Method", Units: "requests/s", Type: registry.Stacked, Priority: 52440,
			Dimensions: []*registry.Dimension{
				{ID: "GET", Algorithm: inc}, {ID: "POST", Algorithm: inc}, {ID: "other", Algorithm: inc}}},
	} {
		c.Family, c.Plugin, c.Module = "web_log", "weblog", "weblog"
		reg.AddChart(c)
	}
	return nil
}

func (w *weblogCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	_ = ctx
	lines, err := w.cursor.lines()
	if err != nil {
		return err
	}
	for _, line := range lines {
		w.ingest(line)
	}
	t := w.totals
	_ = reg.Collect("web_log.requests", now, map[string]float64{"requests": t.requests})
	_ = reg.Collect("web_log.type_requests", now, map[string]float64{
		"success": t.success, "bad": t.bad, "redirect": t.redirect, "error": t.err})
	_ = reg.Collect("web_log.status_code_class_responses", now, map[string]float64{
		"1xx": t.c1, "2xx": t.c2, "3xx": t.c3, "4xx": t.c4, "5xx": t.c5})
	_ = reg.Collect("web_log.bandwidth", now, map[string]float64{"sent": t.bytesSent})
	_ = reg.Collect("web_log.http_method_requests", now, map[string]float64{
		"GET": t.get, "POST": t.post, "other": t.other})
	return nil
}

var combinedLogRE = regexp.MustCompile(`^\S+ \S+ \S+ \[[^\]]+] "(\S+)\s+[^\"]*" (\d{3}) (\d+|-)`)

func (w *weblogCollector) ingest(line string) {
	m := combinedLogRE.FindStringSubmatch(line)
	if m == nil {
		w.totals.unmatched++
		return
	}
	w.totals.requests++
	method := strings.ToUpper(m[1])
	switch method {
	case "GET":
		w.totals.get++
	case "POST":
		w.totals.post++
	default:
		w.totals.other++
	}
	cls := httpStatusClass(m[2])
	switch cls {
	case "1xx":
		w.totals.c1++
	case "2xx":
		w.totals.c2++
		w.totals.success++
	case "3xx":
		w.totals.c3++
		w.totals.redirect++
	case "4xx":
		w.totals.c4++
		w.totals.bad++
	case "5xx":
		w.totals.c5++
		w.totals.err++
	}
	if m[3] != "-" {
		w.totals.bytesSent += firstFloat(m[3])
	}
}

func parseCombinedLog(line string) (method, code, size string, ok bool) {
	m := combinedLogRE.FindStringSubmatch(line)
	if m == nil {
		return "", "", "", false
	}
	return m[1], m[2], m[3], true
}
