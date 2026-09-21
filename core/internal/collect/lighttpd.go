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

// lighttpdConfig is collectors.modules.lighttpd (server-status?auto).
type lighttpdConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type lighttpdCollector struct {
	cfg    lighttpdConfig
	client *http.Client
}

func init() {
	Register("lighttpd", func() Collector { return &lighttpdCollector{} })
}

func (l *lighttpdCollector) Name() string { return "lighttpd" }

func (l *lighttpdCollector) Configure(decode func(v any) error) error {
	if err := decode(&l.cfg); err != nil {
		return err
	}
	if l.cfg.URL == "" {
		l.cfg.URL = "http://127.0.0.1/server-status?auto"
	}
	if l.cfg.Timeout <= 0 {
		l.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (l *lighttpdCollector) Init(reg *registry.Registry) error {
	if l.cfg.URL == "" {
		if err := l.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	l.client = &http.Client{Timeout: l.cfg.Timeout}
	if _, err := l.fetch(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "lighttpd.requests", Title: "Lighttpd requests", Units: "requests/s", Priority: 42100,
			Dimensions: []*registry.Dimension{{ID: "requests", Algorithm: inc}}},
		{ID: "lighttpd.net", Title: "Lighttpd traffic", Units: "KiB/s", Type: registry.Area, Priority: 42110,
			Dimensions: []*registry.Dimension{{ID: "sent", Algorithm: inc, Divisor: 1024}}},
		{ID: "lighttpd.workers", Title: "Lighttpd workers", Units: "servers", Type: registry.Stacked, Priority: 42120,
			Dimensions: []*registry.Dimension{{ID: "busy"}, {ID: "idle"}}},
		{ID: "lighttpd.uptime", Title: "Lighttpd uptime", Units: "seconds", Priority: 42130,
			Dimensions: []*registry.Dimension{{ID: "uptime"}}},
		{ID: "lighttpd.scoreboard", Title: "Lighttpd scoreboard", Units: "connections", Type: registry.Stacked, Priority: 42140},
	} {
		c.Family, c.Plugin, c.Module = "lighttpd", "lighttpd", "lighttpd"
		reg.AddChart(c)
	}
	return nil
}

var lighttpdScore = []struct{ key, letter string }{
	{"waiting", "."}, {"connect", "C"}, {"close", "E"}, {"hard_error", "k"},
	{"read", "r"}, {"read_post", "R"}, {"write", "W"}, {"handle_request", "h"},
	{"request_start", "q"}, {"request_end", "Q"},
}

func (l *lighttpdCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	st, err := l.fetch(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("lighttpd.requests", now, map[string]float64{"requests": st.num["Total Accesses"]})
	_ = reg.Collect("lighttpd.net", now, map[string]float64{"sent": st.num["Total kBytes"] * 1024})
	_ = reg.Collect("lighttpd.workers", now, map[string]float64{"busy": st.num["BusyServers"], "idle": st.num["IdleServers"]})
	_ = reg.Collect("lighttpd.uptime", now, map[string]float64{"uptime": st.num["Uptime"]})
	if ch, ok := reg.Chart("lighttpd.scoreboard"); ok {
		sb := parseLighttpdScoreboard(st.scoreboard)
		for _, d := range lighttpdScore {
			if ch.Dimension(d.key) == nil {
				ch.AddDimension(&registry.Dimension{ID: d.key})
			}
		}
		_ = reg.Collect("lighttpd.scoreboard", now, sb)
	}
	return nil
}

type lighttpdStatus struct {
	num        map[string]float64
	scoreboard string
}

func (l *lighttpdCollector) fetch(ctx context.Context) (lighttpdStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.cfg.URL, nil)
	if err != nil {
		return lighttpdStatus{}, err
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return lighttpdStatus{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return lighttpdStatus{}, fmt.Errorf("lighttpd: %s -> %s", l.cfg.URL, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return lighttpdStatus{}, err
	}
	return parseLighttpdStatus(string(body))
}

func parseLighttpdStatus(s string) (lighttpdStatus, error) {
	out := lighttpdStatus{num: map[string]float64{}}
	if !strings.Contains(s, "BusyServers") {
		return out, fmt.Errorf("lighttpd: not a server-status?auto body")
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

func parseLighttpdScoreboard(s string) map[string]float64 {
	out := map[string]float64{}
	for _, d := range lighttpdScore {
		out[d.key] = 0
	}
	for _, r := range s {
		for _, d := range lighttpdScore {
			if string(r) == d.letter {
				out[d.key]++
				break
			}
		}
	}
	return out
}
