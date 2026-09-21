package collect

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

type s3checkBucket struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
}

// s3checkConfig is collectors.modules.s3check (HTTP GET/HEAD of object URLs).
type s3checkConfig struct {
	Buckets []s3checkBucket `yaml:"buckets"`
	Timeout time.Duration   `yaml:"timeout"`
}

type s3checkCollector struct {
	cfg    s3checkConfig
	client *http.Client
	seen   map[string]bool
}

func init() {
	Register("s3check", func() Collector { return &s3checkCollector{} })
}

func (s *s3checkCollector) Name() string { return "s3check" }

func (s *s3checkCollector) Configure(decode func(v any) error) error {
	if err := decode(&s.cfg); err != nil {
		return err
	}
	if s.cfg.Timeout <= 0 {
		s.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (s *s3checkCollector) Init(reg *registry.Registry) error {
	if s.cfg.Timeout <= 0 {
		if err := s.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if len(s.cfg.Buckets) == 0 {
		return fmt.Errorf("s3check: no buckets")
	}
	s.client = &http.Client{Timeout: s.cfg.Timeout}
	s.seen = map[string]bool{}
	st, err := s.probe(context.Background())
	if err != nil {
		return err
	}
	ok := false
	for _, v := range st {
		if v["success"] > 0 {
			ok = true
			break
		}
	}
	if !ok {
		return fmt.Errorf("s3check: all checks failed")
	}
	return nil
}

func (s *s3checkCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	st, err := s.probe(ctx)
	if err != nil {
		return err
	}
	for id, v := range st {
		if !s.seen[id] {
			s.seen[id] = true
			for _, ch := range []*registry.Chart{
				{ID: "s3check.status." + id, Context: "s3check.status", Title: "S3 check status", Units: "status", Priority: 61700,
					Dimensions: []*registry.Dimension{{ID: "success"}, {ID: "failed"}}},
				{ID: "s3check.time." + id, Context: "s3check.time", Title: "S3 check response time", Units: "ms", Priority: 61710,
					Dimensions: []*registry.Dimension{{ID: "time"}}},
			} {
				ch.Family, ch.Plugin, ch.Module = "s3check", "s3check", "s3check"
				reg.AddChart(ch)
			}
		}
		ok := 0.0
		if v["success"] > 0 {
			ok = 1
		}
		_ = reg.Collect("s3check.status."+id, now, map[string]float64{"success": ok, "failed": 1 - ok})
		_ = reg.Collect("s3check.time."+id, now, map[string]float64{"time": v["time"]})
	}
	return nil
}

func (s *s3checkCollector) probe(ctx context.Context) (map[string]map[string]float64, error) {
	out := map[string]map[string]float64{}
	var last error
	ok := 0
	for _, bkt := range s.cfg.Buckets {
		if bkt.URL == "" {
			continue
		}
		id := sanitizeID(bkt.Name)
		if id == "" {
			id = sanitizeID(bkt.URL)
		}
		start := time.Now()
		_, err := httpGet(ctx, s.client, bkt.URL)
		ms := float64(time.Since(start).Milliseconds())
		if err != nil {
			last = err
			out[id] = map[string]float64{"success": 0, "time": ms}
			continue
		}
		out[id] = map[string]float64{"success": 1, "time": ms}
		ok++
	}
	if len(out) == 0 {
		if last == nil {
			last = fmt.Errorf("no buckets")
		}
		return nil, fmt.Errorf("s3check: %w", last)
	}
	return out, nil
}
