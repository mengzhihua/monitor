package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// puppetConfig is collectors.modules.puppet (status API).
type puppetConfig struct {
	TLS     CollectorTLS  `yaml:"tls"`
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type puppetCollector struct {
	cfg    puppetConfig
	client *http.Client
	url    string
}

func init() {
	Register("puppet", func() Collector { return &puppetCollector{} })
}

func (p *puppetCollector) Name() string { return "puppet" }

func (p *puppetCollector) Configure(decode func(v any) error) error {
	if err := decode(&p.cfg); err != nil {
		return err
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (p *puppetCollector) Init(reg *registry.Registry) error {
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
		base = "https://127.0.0.1:8140"
	}
	p.url = base
	if _, err := p.status(context.Background()); err != nil {
		return err
	}
	mib := int64(1 << 20)
	for _, ch := range []*registry.Chart{
		{ID: "puppet.jvm_heap", Title: "JVM Heap", Units: "MiB", Type: registry.Area, Priority: 59100,
			Dimensions: []*registry.Dimension{{ID: "committed", Divisor: mib}, {ID: "used", Divisor: mib}}},
		{ID: "puppet.jvm_nonheap", Title: "JVM Non-Heap", Units: "MiB", Type: registry.Area, Priority: 59110,
			Dimensions: []*registry.Dimension{{ID: "committed", Divisor: mib}, {ID: "used", Divisor: mib}}},
		{ID: "puppet.cpu", Title: "CPU usage", Units: "percentage", Type: registry.Stacked, Priority: 59120,
			Dimensions: []*registry.Dimension{{ID: "execution"}, {ID: "GC"}}},
		{ID: "puppet.fdopen", Title: "File Descriptors", Units: "descriptors", Priority: 59130,
			Dimensions: []*registry.Dimension{{ID: "used"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "puppet", "puppet", "puppet"
		reg.AddChart(ch)
	}
	return nil
}

func (p *puppetCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := p.status(ctx)
	if err != nil {
		return err
	}
	heap := puppetJVM(m, "heap-memory")
	non := puppetJVM(m, "non-heap-memory")
	_ = reg.Collect("puppet.jvm_heap", now, map[string]float64{"committed": heap.committed, "used": heap.used})
	_ = reg.Collect("puppet.jvm_nonheap", now, map[string]float64{"committed": non.committed, "used": non.used})
	cpu := nestFloat(m, "status-service", "status", "experimental", "metrics", "cpu-usage", "used")
	if cpu == 0 {
		cpu = nestFloat(m, "status-service", "status", "experimental", "jvm-metrics", "cpu-usage", "used")
	}
	gc := nestFloat(m, "status-service", "status", "experimental", "metrics", "gc-cpu-usage", "used")
	_ = reg.Collect("puppet.cpu", now, map[string]float64{"execution": cpu * 100, "GC": gc * 100})
	fd := nestFloat(m, "status-service", "status", "experimental", "jvm-metrics", "file-descriptors", "used")
	if fd == 0 {
		fd = nestFloat(m, "status-service", "status", "experimental", "metrics", "file-descriptors", "used")
	}
	_ = reg.Collect("puppet.fdopen", now, map[string]float64{"used": fd})
	return nil
}

type puppetMem struct{ committed, used float64 }

func puppetJVM(m map[string]any, kind string) puppetMem {
	base := nestMap(m, "status-service", "status", "experimental", "jvm-metrics", kind)
	if base == nil {
		base = nestMap(m, "status-service", "status", "jvm-metrics", kind)
	}
	return puppetMem{committed: nestFloat(base, "committed"), used: nestFloat(base, "used")}
}

func (p *puppetCollector) status(ctx context.Context) (map[string]any, error) {
	u := p.url + "/status/v1/services?level=debug"
	b, err := httpGet(ctx, p.client, u)
	if err != nil {
		return nil, fmt.Errorf("puppet: %w", err)
	}
	m, err := jsonMap(b)
	if err != nil {
		return nil, fmt.Errorf("puppet: %w", err)
	}
	if nestMap(m, "status-service") == nil {
		return nil, fmt.Errorf("puppet: no status-service")
	}
	return m, nil
}
