package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// goExpvarConfig is collectors.modules.go_expvar (python.d go_expvar /debug/vars).
type goExpvarConfig struct {
	URL             string        `yaml:"url"`
	CollectMemstats *bool         `yaml:"collect_memstats"`
	Timeout         time.Duration `yaml:"timeout"`
}

type goExpvarCollector struct {
	cfg    goExpvarConfig
	client *http.Client
	get    func(ctx context.Context) ([]byte, error)
}

func init() {
	Register("go_expvar", func() Collector { return &goExpvarCollector{} })
}

func (g *goExpvarCollector) Name() string { return "go_expvar" }

func (g *goExpvarCollector) Configure(decode func(v any) error) error {
	if err := decode(&g.cfg); err != nil {
		return err
	}
	if g.cfg.URL == "" {
		g.cfg.URL = "http://127.0.0.1:6060/debug/vars"
	}
	if g.cfg.Timeout <= 0 {
		g.cfg.Timeout = 3 * time.Second
	}
	if g.cfg.CollectMemstats == nil {
		t := true
		g.cfg.CollectMemstats = &t
	}
	return nil
}

func (g *goExpvarCollector) Init(reg *registry.Registry) error {
	if g.cfg.URL == "" {
		if err := g.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if g.get == nil {
		g.client = &http.Client{Timeout: g.cfg.Timeout}
		g.get = g.httpGet
	}
	if _, err := g.sample(context.Background()); err != nil {
		return err
	}
	kib := int64(1024)
	for _, ch := range []*registry.Chart{
		{ID: "expvar.memstats.heap", Context: "expvar.memstats.heap", Title: "memory: size of heap memory structures", Units: "KiB", Family: "memstats", Priority: 64600,
			Dimensions: []*registry.Dimension{{ID: "alloc", Divisor: kib}, {ID: "inuse", Divisor: kib}}},
		{ID: "expvar.memstats.stack", Context: "expvar.memstats.stack", Title: "memory: size of stack memory structures", Units: "KiB", Family: "memstats", Priority: 64610,
			Dimensions: []*registry.Dimension{{ID: "inuse", Divisor: kib}}},
		{ID: "expvar.memstats.mspan", Context: "expvar.memstats.mspan", Title: "memory: size of mspan memory structures", Units: "KiB", Family: "memstats", Priority: 64620,
			Dimensions: []*registry.Dimension{{ID: "inuse", Divisor: kib}}},
		{ID: "expvar.memstats.mcache", Context: "expvar.memstats.mcache", Title: "memory: size of mcache memory structures", Units: "KiB", Family: "memstats", Priority: 64630,
			Dimensions: []*registry.Dimension{{ID: "inuse", Divisor: kib}}},
		{ID: "expvar.memstats.sys", Context: "expvar.memstats.sys", Title: "memory: size of reserved virtual address space", Units: "KiB", Family: "memstats", Priority: 64640,
			Dimensions: []*registry.Dimension{{ID: "sys", Divisor: kib}}},
		{ID: "expvar.memstats.live_objects", Context: "expvar.memstats.live_objects", Title: "memory: number of live objects", Units: "objects", Family: "memstats", Priority: 64650,
			Dimensions: []*registry.Dimension{{ID: "live"}}},
		{ID: "expvar.memstats.gc_pauses", Context: "expvar.memstats.gc_pauses", Title: "memory: average duration of GC pauses", Units: "ns", Family: "memstats", Priority: 64660,
			Dimensions: []*registry.Dimension{{ID: "avg"}}},
	} {
		ch.Plugin, ch.Module = "python.d", "go_expvar"
		reg.AddChart(ch)
	}
	return nil
}

func (g *goExpvarCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	s, err := g.sample(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("expvar.memstats.heap", now, map[string]float64{"alloc": s.HeapAlloc, "inuse": s.HeapInuse})
	_ = reg.Collect("expvar.memstats.stack", now, map[string]float64{"inuse": s.StackInuse})
	_ = reg.Collect("expvar.memstats.mspan", now, map[string]float64{"inuse": s.MSpanInuse})
	_ = reg.Collect("expvar.memstats.mcache", now, map[string]float64{"inuse": s.MCacheInuse})
	_ = reg.Collect("expvar.memstats.sys", now, map[string]float64{"sys": s.Sys})
	_ = reg.Collect("expvar.memstats.live_objects", now, map[string]float64{"live": s.Live})
	_ = reg.Collect("expvar.memstats.gc_pauses", now, map[string]float64{"avg": s.GCPAuseAvg})
	return nil
}

type expvarMemstats struct {
	HeapAlloc, HeapInuse, StackInuse, MSpanInuse, MCacheInuse, Sys, Live, GCPAuseAvg float64
}

func (g *goExpvarCollector) httpGet(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.cfg.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

func (g *goExpvarCollector) sample(ctx context.Context) (expvarMemstats, error) {
	b, err := g.get(ctx)
	if err != nil {
		return expvarMemstats{}, fmt.Errorf("go_expvar: %w", err)
	}
	s, err := parseExpvarMemstats(b)
	if err != nil {
		return expvarMemstats{}, err
	}
	return s, nil
}

func parseExpvarMemstats(b []byte) (expvarMemstats, error) {
	var root struct {
		Memstats struct {
			HeapAlloc   float64   `json:"HeapAlloc"`
			HeapInuse   float64   `json:"HeapInuse"`
			StackInuse  float64   `json:"StackInuse"`
			MSpanInuse  float64   `json:"MSpanInuse"`
			MCacheInuse float64   `json:"MCacheInuse"`
			Sys         float64   `json:"Sys"`
			Mallocs     float64   `json:"Mallocs"`
			Frees       float64   `json:"Frees"`
			PauseNs     []float64 `json:"PauseNs"`
		} `json:"memstats"`
	}
	if err := json.Unmarshal(b, &root); err != nil {
		return expvarMemstats{}, fmt.Errorf("go_expvar: %w", err)
	}
	m := root.Memstats
	if m.Sys == 0 && m.HeapAlloc == 0 && m.Mallocs == 0 {
		return expvarMemstats{}, fmt.Errorf("go_expvar: no memstats")
	}
	var pauseSum, pauseN float64
	for _, p := range m.PauseNs {
		if p > 0 {
			pauseSum += p
			pauseN++
		}
	}
	avg := 0.0
	if pauseN > 0 {
		avg = pauseSum / pauseN
	}
	return expvarMemstats{
		HeapAlloc: m.HeapAlloc, HeapInuse: m.HeapInuse, StackInuse: m.StackInuse,
		MSpanInuse: m.MSpanInuse, MCacheInuse: m.MCacheInuse, Sys: m.Sys,
		Live: m.Mallocs - m.Frees, GCPAuseAvg: avg,
	}, nil
}
