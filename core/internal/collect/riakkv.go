package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// riakkvConfig is collectors.modules.riakkv (HTTP /stats).
type riakkvConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type riakkvCollector struct {
	cfg    riakkvConfig
	client *http.Client
	url    string
}

func init() {
	Register("riakkv", func() Collector { return &riakkvCollector{} })
}

func (r *riakkvCollector) Name() string { return "riakkv" }

func (r *riakkvCollector) Configure(decode func(v any) error) error {
	if err := decode(&r.cfg); err != nil {
		return err
	}
	if r.cfg.Timeout <= 0 {
		r.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (r *riakkvCollector) Init(reg *registry.Registry) error {
	if r.cfg.Timeout <= 0 {
		if err := r.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	r.client = &http.Client{Timeout: r.cfg.Timeout}
	base := strings.TrimRight(r.cfg.URL, "/")
	if base == "" {
		base = "http://127.0.0.1:8098/stats"
	}
	r.url = base
	if _, err := r.stats(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "riak.kv.throughput", Title: "Reads & writes coordinated by this node", Units: "operations/s", Priority: 60300,
			Dimensions: []*registry.Dimension{{ID: "gets", Algorithm: inc}, {ID: "puts", Algorithm: inc}}},
		{ID: "riak.kv.latency.get", Title: "GET latency", Units: "ms", Priority: 60310,
			Dimensions: []*registry.Dimension{{ID: "mean", Divisor: 1000}, {ID: "median", Divisor: 1000}, {ID: "95", Divisor: 1000}}},
		{ID: "riak.vm.processes.count", Title: "Total processes running in the Erlang VM", Units: "processes", Priority: 60320,
			Dimensions: []*registry.Dimension{{ID: "processes"}}},
		{ID: "riak.core.protobuf_connections", Title: "Protocol buffer connections by status", Units: "connections", Priority: 60330,
			Dimensions: []*registry.Dimension{{ID: "active"}}},
		{ID: "riak.core.fsm_active", Title: "Active finite state machines by kind", Units: "fsms", Priority: 60340,
			Dimensions: []*registry.Dimension{{ID: "get"}, {ID: "put"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "riakkv", "riakkv", "riakkv"
		reg.AddChart(ch)
	}
	return nil
}

func (r *riakkvCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := r.stats(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("riak.kv.throughput", now, map[string]float64{"gets": nestFloat(m, "node_gets_total"), "puts": nestFloat(m, "node_puts_total")})
	_ = reg.Collect("riak.kv.latency.get", now, map[string]float64{
		"mean": nestFloat(m, "node_get_fsm_time_mean"), "median": nestFloat(m, "node_get_fsm_time_median"), "95": nestFloat(m, "node_get_fsm_time_95"),
	})
	_ = reg.Collect("riak.vm.processes.count", now, map[string]float64{"processes": nestFloat(m, "sys_processes")})
	_ = reg.Collect("riak.core.protobuf_connections", now, map[string]float64{"active": nestFloat(m, "pbc_active")})
	_ = reg.Collect("riak.core.fsm_active", now, map[string]float64{"get": nestFloat(m, "node_get_fsm_active"), "put": nestFloat(m, "node_put_fsm_active")})
	return nil
}

func (r *riakkvCollector) stats(ctx context.Context) (map[string]any, error) {
	b, err := httpGet(ctx, r.client, r.url)
	if err != nil {
		return nil, fmt.Errorf("riakkv: %w", err)
	}
	m, err := jsonMap(b)
	if err != nil {
		return nil, fmt.Errorf("riakkv: %w", err)
	}
	if _, ok := m["node_gets_total"]; !ok && nestFloat(m, "pbc_active") == 0 && nestFloat(m, "sys_processes") == 0 {
		return nil, fmt.Errorf("riakkv: no stats")
	}
	return m, nil
}
