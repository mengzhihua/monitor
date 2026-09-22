package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// ecsConfig is collectors.modules.ecs (AWS ECS task metadata v4).
type ecsConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type ecsCollector struct {
	cfg    ecsConfig
	client *http.Client
	get    func(ctx context.Context, path string) ([]byte, error)
	seen   map[string]bool
}

func init() {
	Register("ecs", func() Collector { return &ecsCollector{} })
}

func (e *ecsCollector) Name() string { return "ecs" }

func (e *ecsCollector) Configure(decode func(v any) error) error {
	if err := decode(&e.cfg); err != nil {
		return err
	}
	if e.cfg.URL == "" {
		e.cfg.URL = strings.TrimRight(os.Getenv("ECS_CONTAINER_METADATA_URI_V4"), "/")
		if e.cfg.URL == "" {
			e.cfg.URL = strings.TrimRight(os.Getenv("ECS_CONTAINER_METADATA_URI"), "/")
		}
	}
	if e.cfg.Timeout <= 0 {
		e.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (e *ecsCollector) Init(reg *registry.Registry) error {
	if e.cfg.Timeout == 0 {
		if err := e.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if e.cfg.URL == "" {
		return fmt.Errorf("ecs: no metadata URL")
	}
	if e.get == nil {
		e.client = &http.Client{Timeout: e.cfg.Timeout}
		e.get = e.httpGet
	}
	if _, err := e.task(context.Background()); err != nil {
		return err
	}
	e.seen = map[string]bool{}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "ecs.containers", Context: "ecs.containers", Title: "ECS task containers", Units: "containers", Family: "ecs", Type: registry.Stacked, Priority: 30300,
			Dimensions: []*registry.Dimension{{ID: "running"}, {ID: "stopped"}, {ID: "other"}}},
		{ID: "ecs.cpu", Context: "ecs.cpu", Title: "ECS task CPU", Units: "nanoseconds", Family: "ecs", Priority: 30310,
			Dimensions: []*registry.Dimension{{ID: "usage", Algorithm: inc}}},
		{ID: "ecs.mem", Context: "ecs.mem", Title: "ECS task memory", Units: "MiB", Family: "ecs", Type: registry.Stacked, Priority: 30320,
			Dimensions: []*registry.Dimension{
				{ID: "rss", Divisor: 1024 * 1024}, {ID: "limit", Divisor: 1024 * 1024, Hidden: true}}},
	} {
		ch.Plugin, ch.Module = "ecs", "ecs"
		reg.AddChart(ch)
	}
	return nil
}

func (e *ecsCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	t, err := e.task(ctx)
	if err != nil {
		return err
	}
	states := map[string]float64{"running": 0, "stopped": 0, "other": 0}
	for _, c := range t.Containers {
		st := strings.ToUpper(c.KnownStatus)
		switch st {
		case "RUNNING":
			states["running"]++
		case "STOPPED", "STOPPING":
			states["stopped"]++
		default:
			states["other"]++
		}
		id := "ecs.container_state." + sanitizeID(c.Name)
		if !e.seen[id] {
			e.seen[id] = true
			ch := sysChart(id, "ecs", "ECS container "+c.Name, "boolean", 30330, &registry.Dimension{ID: "running"})
			ch.Context, ch.Plugin, ch.Module = "ecs.container_state", "ecs", "ecs"
			reg.AddChart(ch)
		}
		run := 0.0
		if st == "RUNNING" {
			run = 1
		}
		_ = reg.Collect(id, now, map[string]float64{"running": run})
	}
	_ = reg.Collect("ecs.containers", now, states)

	st, err := e.stats(ctx)
	if err == nil {
		_ = reg.Collect("ecs.cpu", now, map[string]float64{"usage": st.CPU})
		_ = reg.Collect("ecs.mem", now, map[string]float64{"rss": st.Mem, "limit": st.Limit})
	}
	return nil
}

func (e *ecsCollector) Functions() []Function {
	return []Function{{
		Name: "ecs-containers", Help: "ECS task containers (name, status, image)", Timeout: 10,
		Run: func(ctx context.Context, args map[string]string) (any, error) {
			t, err := e.task(ctx)
			if err != nil {
				return Table{}, err
			}
			out := Table{Columns: []string{"name", "status", "image"}, Total: len(t.Containers)}
			out.Rows = make([]any, len(t.Containers))
			for i, c := range t.Containers {
				out.Rows[i] = map[string]string{"name": c.Name, "status": c.KnownStatus, "image": c.Image}
			}
			return out, nil
		},
	}}
}

type ecsTask struct {
	Family      string         `json:"Family"`
	KnownStatus string         `json:"KnownStatus"`
	Containers  []ecsContainer `json:"Containers"`
}

type ecsContainer struct {
	Name        string `json:"Name"`
	KnownStatus string `json:"KnownStatus"`
	Image       string `json:"Image"`
}

type ecsStatsSnap struct {
	CPU, Mem, Limit float64
}

func (e *ecsCollector) httpGet(ctx context.Context, path string) ([]byte, error) {
	u := e.cfg.URL
	if path != "" {
		u = strings.TrimRight(e.cfg.URL, "/") + path
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("ecs: %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

func (e *ecsCollector) task(ctx context.Context) (ecsTask, error) {
	b, err := e.get(ctx, "/task")
	if err != nil {
		// some endpoints already ARE the task document
		b, err = e.get(ctx, "")
		if err != nil {
			return ecsTask{}, fmt.Errorf("ecs: %w", err)
		}
	}
	var t ecsTask
	if err := json.Unmarshal(b, &t); err != nil {
		return ecsTask{}, fmt.Errorf("ecs: %w", err)
	}
	if len(t.Containers) == 0 {
		return ecsTask{}, fmt.Errorf("ecs: no containers")
	}
	return t, nil
}

func (e *ecsCollector) stats(ctx context.Context) (ecsStatsSnap, error) {
	b, err := e.get(ctx, "/task/stats")
	if err != nil {
		b, err = e.get(ctx, "/stats")
		if err != nil {
			return ecsStatsSnap{}, err
		}
	}
	return parseECSStats(b), nil
}

func parseECSStats(b []byte) ecsStatsSnap {
	var one struct {
		CPUStats struct {
			CPUUsage struct {
				TotalUsage float64 `json:"total_usage"`
			} `json:"cpu_usage"`
		} `json:"cpu_stats"`
		MemoryStats struct {
			Usage float64 `json:"usage"`
			Limit float64 `json:"limit"`
		} `json:"memory_stats"`
	}
	if json.Unmarshal(b, &one) == nil && (one.CPUStats.CPUUsage.TotalUsage != 0 || one.MemoryStats.Usage != 0) {
		return ecsStatsSnap{CPU: one.CPUStats.CPUUsage.TotalUsage, Mem: one.MemoryStats.Usage, Limit: one.MemoryStats.Limit}
	}
	var many map[string]json.RawMessage
	if json.Unmarshal(b, &many) != nil {
		return ecsStatsSnap{}
	}
	var sum ecsStatsSnap
	for _, raw := range many {
		s := parseECSStats(raw)
		sum.CPU += s.CPU
		sum.Mem += s.Mem
		if s.Limit > sum.Limit {
			sum.Limit = s.Limit
		}
	}
	return sum
}
