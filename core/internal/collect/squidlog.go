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

// squidlogConfig is collectors.modules.squidlog (native access.log).
type squidlogConfig struct {
	Path string `yaml:"path"`
}

type squidlogCollector struct {
	cfg    squidlogConfig
	cursor *fileCursor
	totals squidlogTotals
}

type squidlogTotals struct {
	requests, unmatched, bytesSent float64
	c0, c1, c2, c3, c4, c5         float64
	success, bad, redirect, err    float64
	hit, miss, other               float64
}

func init() {
	Register("squidlog", func() Collector { return &squidlogCollector{} })
}

func (s *squidlogCollector) Name() string { return "squidlog" }

func (s *squidlogCollector) Configure(decode func(v any) error) error {
	return decode(&s.cfg)
}

func (s *squidlogCollector) Init(reg *registry.Registry) error {
	_ = s.Configure(func(any) error { return nil })
	path := s.cfg.Path
	if path == "" {
		for _, p := range []string{"/var/log/squid/access.log", "/var/log/squid3/access.log"} {
			if _, err := os.Stat(p); err == nil {
				path = p
				break
			}
		}
	}
	if path == "" {
		return fmt.Errorf("squidlog: no access log")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("squidlog: %w", err)
	}
	s.cursor = &fileCursor{path: path}
	if err := s.cursor.skipToEnd(); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "squidlog.requests", Title: "Total Requests", Units: "requests/s", Priority: 52500,
			Dimensions: []*registry.Dimension{{ID: "requests", Algorithm: inc}}},
		{ID: "squidlog.type_requests", Title: "Requests By Type", Units: "requests/s", Type: registry.Stacked, Priority: 52510,
			Dimensions: []*registry.Dimension{
				{ID: "success", Algorithm: inc}, {ID: "bad", Algorithm: inc},
				{ID: "redirect", Algorithm: inc}, {ID: "error", Algorithm: inc}}},
		{ID: "squidlog.http_status_code_class_responses", Title: "Responses By HTTP Status Code Class", Units: "responses/s", Type: registry.Stacked, Priority: 52520,
			Dimensions: []*registry.Dimension{
				{ID: "2xx", Algorithm: inc}, {ID: "5xx", Algorithm: inc}, {ID: "3xx", Algorithm: inc},
				{ID: "4xx", Algorithm: inc}, {ID: "1xx", Algorithm: inc}, {ID: "0xx", Algorithm: inc}}},
		{ID: "squidlog.bandwidth", Title: "Bandwidth", Units: "kilobits/s", Priority: 52530,
			Dimensions: []*registry.Dimension{{ID: "sent", Algorithm: inc, Divisor: 1000}}},
		{ID: "squidlog.cache_result_code_requests", Title: "Requests By Cache Result Code", Units: "requests/s", Type: registry.Stacked, Priority: 52540,
			Dimensions: []*registry.Dimension{
				{ID: "HIT", Algorithm: inc}, {ID: "MISS", Algorithm: inc}, {ID: "other", Algorithm: inc}}},
	} {
		c.Family, c.Plugin, c.Module = "squidlog", "squidlog", "squidlog"
		reg.AddChart(c)
	}
	return nil
}

func (s *squidlogCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	_ = ctx
	lines, err := s.cursor.lines()
	if err != nil {
		return err
	}
	for _, line := range lines {
		s.ingest(line)
	}
	t := s.totals
	_ = reg.Collect("squidlog.requests", now, map[string]float64{"requests": t.requests})
	_ = reg.Collect("squidlog.type_requests", now, map[string]float64{
		"success": t.success, "bad": t.bad, "redirect": t.redirect, "error": t.err})
	_ = reg.Collect("squidlog.http_status_code_class_responses", now, map[string]float64{
		"0xx": t.c0, "1xx": t.c1, "2xx": t.c2, "3xx": t.c3, "4xx": t.c4, "5xx": t.c5})
	_ = reg.Collect("squidlog.bandwidth", now, map[string]float64{"sent": t.bytesSent})
	_ = reg.Collect("squidlog.cache_result_code_requests", now, map[string]float64{
		"HIT": t.hit, "MISS": t.miss, "other": t.other})
	return nil
}

// timestamp elapsed client code/status size method URL ...
var squidLogRE = regexp.MustCompile(`^\S+\s+\S+\s+\S+\s+(\S+)/(\d+)\s+(\d+)\s+(\S+)`)

func (s *squidlogCollector) ingest(line string) {
	m := squidLogRE.FindStringSubmatch(line)
	if m == nil {
		s.totals.unmatched++
		return
	}
	s.totals.requests++
	code := m[1]
	switch {
	case strings.Contains(code, "HIT"):
		s.totals.hit++
	case strings.Contains(code, "MISS"):
		s.totals.miss++
	default:
		s.totals.other++
	}
	cls := httpStatusClass(m[2])
	switch cls {
	case "1xx":
		s.totals.c1++
	case "2xx":
		s.totals.c2++
		s.totals.success++
	case "3xx":
		s.totals.c3++
		s.totals.redirect++
	case "4xx":
		s.totals.c4++
		s.totals.bad++
	case "5xx":
		s.totals.c5++
		s.totals.err++
	default:
		s.totals.c0++
	}
	s.totals.bytesSent += firstFloat(m[3])
}

func parseSquidLog(line string) (code, status, size, method string, ok bool) {
	m := squidLogRE.FindStringSubmatch(line)
	if m == nil {
		return "", "", "", "", false
	}
	return m[1], m[2], m[3], m[4], true
}
