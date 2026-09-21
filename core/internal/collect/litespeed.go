package collect

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// litespeedConfig is collectors.modules.litespeed (.rtreport under reports_dir).
type litespeedConfig struct {
	ReportsDir string `yaml:"reports_dir"`
}

type litespeedCollector struct {
	cfg      litespeedConfig
	readDir  func(dir string) ([]string, error)
	readFile func(path string) ([]byte, error)
	dir      string
}

func init() {
	Register("litespeed", func() Collector { return &litespeedCollector{} })
}

func (l *litespeedCollector) Name() string { return "litespeed" }

func (l *litespeedCollector) Configure(decode func(v any) error) error {
	return decode(&l.cfg)
}

func (l *litespeedCollector) Init(reg *registry.Registry) error {
	if l.readDir == nil {
		l.readDir = func(dir string) ([]string, error) {
			matches, err := filepath.Glob(filepath.Join(dir, ".rtreport*"))
			if err != nil {
				return nil, err
			}
			return matches, nil
		}
	}
	if l.readFile == nil {
		l.readFile = os.ReadFile
	}
	dir := l.cfg.ReportsDir
	if dir == "" {
		dir = "/tmp/lshttpd"
	}
	l.dir = dir
	if _, err := l.reports(); err != nil {
		return err
	}
	for _, ch := range []*registry.Chart{
		{ID: "litespeed.requests", Title: "Requests", Units: "requests/s", Priority: 60400,
			Dimensions: []*registry.Dimension{{ID: "requests"}}},
		{ID: "litespeed.requests_processing", Title: "Processing requests", Units: "requests", Priority: 60410,
			Dimensions: []*registry.Dimension{{ID: "processing"}}},
		{ID: "litespeed.net_throughput", Title: "HTTP throughput", Units: "kilobits/s", Type: registry.Area, Priority: 60420,
			Dimensions: []*registry.Dimension{{ID: "in"}, {ID: "out", Multiplier: -1}}},
		{ID: "litespeed.net_ssl_throughput", Title: "HTTPs throughput", Units: "kilobits/s", Type: registry.Area, Priority: 60430,
			Dimensions: []*registry.Dimension{{ID: "in"}, {ID: "out", Multiplier: -1}}},
		{ID: "litespeed.connections", Title: "HTTP connections", Units: "connections", Type: registry.Stacked, Priority: 60440,
			Dimensions: []*registry.Dimension{{ID: "free"}, {ID: "used"}}},
		{ID: "litespeed.ssl_connections", Title: "HTTPs connections", Units: "connections", Type: registry.Stacked, Priority: 60450,
			Dimensions: []*registry.Dimension{{ID: "free"}, {ID: "used"}}},
		{ID: "litespeed.public_cache", Title: "Public cache hits", Units: "hits/s", Priority: 60460,
			Dimensions: []*registry.Dimension{{ID: "hits"}}},
		{ID: "litespeed.private_cache", Title: "Private cache hits", Units: "hits/s", Priority: 60470,
			Dimensions: []*registry.Dimension{{ID: "hits"}}},
		{ID: "litespeed.static", Title: "Static hits", Units: "hits/s", Priority: 60480,
			Dimensions: []*registry.Dimension{{ID: "hits"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "litespeed", "litespeed", "litespeed"
		reg.AddChart(ch)
	}
	return nil
}

func (l *litespeedCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	mx, err := l.reports()
	if err != nil {
		return err
	}
	_ = reg.Collect("litespeed.requests", now, map[string]float64{"requests": mx["req_per_sec"]})
	_ = reg.Collect("litespeed.requests_processing", now, map[string]float64{"processing": mx["req_processing"]})
	_ = reg.Collect("litespeed.net_throughput", now, map[string]float64{"in": mx["bps_in"], "out": mx["bps_out"]})
	_ = reg.Collect("litespeed.net_ssl_throughput", now, map[string]float64{"in": mx["ssl_bps_in"], "out": mx["ssl_bps_out"]})
	_ = reg.Collect("litespeed.connections", now, map[string]float64{"free": mx["availconn"], "used": mx["plainconn"]})
	_ = reg.Collect("litespeed.ssl_connections", now, map[string]float64{"free": mx["availssl"], "used": mx["sslconn"]})
	_ = reg.Collect("litespeed.public_cache", now, map[string]float64{"hits": mx["pub_cache_hits_per_sec"]})
	_ = reg.Collect("litespeed.private_cache", now, map[string]float64{"hits": mx["private_cache_hits_per_sec"]})
	_ = reg.Collect("litespeed.static", now, map[string]float64{"hits": mx["static_hits_per_sec"]})
	return nil
}

func (l *litespeedCollector) reports() (map[string]float64, error) {
	files, err := l.readDir(l.dir)
	if err != nil {
		return nil, fmt.Errorf("litespeed: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("litespeed: no reports found")
	}
	mx := map[string]float64{}
	var valid bool
	for _, f := range files {
		b, err := l.readFile(f)
		if err != nil {
			return nil, fmt.Errorf("litespeed: %w", err)
		}
		if parseLitespeedReport(mx, string(b)) {
			valid = true
		}
	}
	if !valid {
		return nil, fmt.Errorf("litespeed: unexpected file: not a litespeed report")
	}
	return mx, nil
}

func parseLitespeedReport(mx map[string]float64, body string) bool {
	valid := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		default:
			continue
		case strings.HasPrefix(line, "BPS_IN:"):
		case strings.HasPrefix(line, "PLAINCONN:"):
		case strings.HasPrefix(line, "MAXCONN:"):
		case strings.HasPrefix(line, "REQ_RATE []:"):
			line = strings.TrimPrefix(line, "REQ_RATE []:")
		}
		for _, part := range strings.Split(line, ",") {
			metric, sVal, ok := strings.Cut(part, ":")
			if !ok {
				continue
			}
			metric, sVal = strings.TrimSpace(metric), strings.TrimSpace(sVal)
			val, err := strconv.ParseFloat(sVal, 64)
			if err != nil {
				continue
			}
			key := strings.ToLower(metric)
			switch metric {
			default:
				continue
			case "REQ_PER_SEC", "PUB_CACHE_HITS_PER_SEC", "PRIVATE_CACHE_HITS_PER_SEC", "STATIC_HITS_PER_SEC":
				mx[key] += val
			case "BPS_IN", "BPS_OUT", "SSL_BPS_IN", "SSL_BPS_OUT":
				mx[key] += val * 8
			case "REQ_PROCESSING", "PLAINCONN", "AVAILCONN", "SSLCONN", "AVAILSSL":
				mx[key] += val
			}
			valid = true
		}
	}
	return valid
}
