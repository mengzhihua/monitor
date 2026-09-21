package collect

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// apacheConfig is collectors.modules.apache (mod_status?auto).
type apacheConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type apacheCollector struct {
	cfg    apacheConfig
	client *http.Client
}

func init() {
	Register("apache", func() Collector { return &apacheCollector{} })
}

func (a *apacheCollector) Name() string { return "apache" }

func (a *apacheCollector) Configure(decode func(v any) error) error {
	if err := decode(&a.cfg); err != nil {
		return err
	}
	if a.cfg.URL == "" {
		a.cfg.URL = "http://127.0.0.1/server-status?auto"
	}
	if a.cfg.Timeout <= 0 {
		a.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (a *apacheCollector) Init(reg *registry.Registry) error {
	if a.cfg.URL == "" {
		if err := a.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	a.client = &http.Client{Timeout: a.cfg.Timeout}
	if _, err := a.fetch(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "apache.connections", Title: "Apache connections", Units: "connections", Priority: 42000,
			Dimensions: []*registry.Dimension{{ID: "connections"}}},
		{ID: "apache.conns_async", Title: "Apache async connections", Units: "connections", Type: registry.Stacked, Priority: 42001,
			Dimensions: []*registry.Dimension{{ID: "writing"}, {ID: "keepalive"}, {ID: "closing"}}},
		{ID: "apache.workers", Title: "Apache workers", Units: "workers", Type: registry.Stacked, Priority: 42010,
			Dimensions: []*registry.Dimension{{ID: "busy"}, {ID: "idle"}}},
		{ID: "apache.requests", Title: "Apache requests", Units: "requests/s", Priority: 42020,
			Dimensions: []*registry.Dimension{{ID: "requests", Algorithm: inc}}},
		{ID: "apache.net", Title: "Apache traffic", Units: "KiB/s", Priority: 42030, Type: registry.Area,
			Dimensions: []*registry.Dimension{{ID: "sent", Algorithm: inc, Divisor: 1024}}},
		{ID: "apache.scoreboard", Title: "Apache scoreboard", Units: "connections", Type: registry.Stacked, Priority: 42040},
	} {
		c.Family, c.Plugin, c.Module = "apache", "apache", "apache"
		reg.AddChart(c)
	}
	return nil
}

var apacheScore = []struct{ key, letter string }{
	{"waiting", "_"}, {"starting", "S"}, {"reading", "R"}, {"sending", "W"},
	{"keepalive", "K"}, {"dns", "D"}, {"closing", "C"}, {"logging", "L"},
	{"finishing", "G"}, {"idle_cleanup", "I"}, {"open", "."},
}

type apacheStatus struct {
	num        map[string]float64
	scoreboard string
}

func (a *apacheCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	st, err := a.fetch(ctx)
	if err != nil {
		return err
	}
	m := st.num
	_ = reg.Collect("apache.connections", now, map[string]float64{"connections": m["ConnsTotal"]})
	_ = reg.Collect("apache.conns_async", now, map[string]float64{
		"writing": m["ConnsAsyncWriting"], "keepalive": m["ConnsAsyncKeepAlive"], "closing": m["ConnsAsyncClosing"]})
	_ = reg.Collect("apache.workers", now, map[string]float64{"busy": m["BusyWorkers"], "idle": m["IdleWorkers"]})
	_ = reg.Collect("apache.requests", now, map[string]float64{"requests": m["Total Accesses"]})
	_ = reg.Collect("apache.net", now, map[string]float64{"sent": m["Total kBytes"] * 1024})
	if ch, ok := reg.Chart("apache.scoreboard"); ok {
		sb := parseApacheScoreboard(st.scoreboard)
		for _, d := range apacheScore {
			if ch.Dimension(d.key) == nil {
				ch.AddDimension(&registry.Dimension{ID: d.key})
			}
		}
		_ = reg.Collect("apache.scoreboard", now, sb)
	}
	return nil
}

func (a *apacheCollector) fetch(ctx context.Context) (apacheStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.URL, nil)
	if err != nil {
		return apacheStatus{}, err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return apacheStatus{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apacheStatus{}, fmt.Errorf("apache: %s -> %s", a.cfg.URL, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return apacheStatus{}, err
	}
	return parseApacheStatus(string(body))
}

func parseApacheStatus(s string) (apacheStatus, error) {
	out := apacheStatus{num: map[string]float64{}}
	if !strings.Contains(s, "BusyWorkers") && !strings.Contains(s, "Total Accesses") {
		return out, fmt.Errorf("apache: not a server-status?auto body")
	}
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "Scoreboard" {
			out.scoreboard = v
			continue
		}
		n, err := strconv.ParseFloat(v, 64)
		if err == nil {
			out.num[k] = n
		}
	}
	return out, nil
}

func parseApacheScoreboard(s string) map[string]float64 {
	out := map[string]float64{}
	for _, d := range apacheScore {
		out[d.key] = 0
	}
	for _, r := range s {
		for _, d := range apacheScore {
			if string(r) == d.letter {
				out[d.key]++
				break
			}
		}
	}
	return out
}
