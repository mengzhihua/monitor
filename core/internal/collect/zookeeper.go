package collect

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// zookeeperConfig is collectors.modules.zookeeper (four-letter mntr).
type zookeeperConfig struct {
	Address string        `yaml:"address"`
	Timeout time.Duration `yaml:"timeout"`
}

type zookeeperCollector struct {
	cfg zookeeperConfig
}

func init() {
	Register("zookeeper", func() Collector { return &zookeeperCollector{} })
}

func (z *zookeeperCollector) Name() string { return "zookeeper" }

func (z *zookeeperCollector) Configure(decode func(v any) error) error {
	if err := decode(&z.cfg); err != nil {
		return err
	}
	if z.cfg.Address == "" {
		z.cfg.Address = "127.0.0.1:2181"
	}
	if z.cfg.Timeout <= 0 {
		z.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (z *zookeeperCollector) Init(reg *registry.Registry) error {
	if z.cfg.Address == "" {
		if err := z.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := z.mntr(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "zookeeper.requests", Title: "Outstanding Requests", Units: "requests", Priority: 51000,
			Dimensions: []*registry.Dimension{{ID: "outstanding"}}},
		{ID: "zookeeper.requests_latency", Title: "Requests Latency", Units: "ms", Priority: 51010,
			Dimensions: []*registry.Dimension{{ID: "min"}, {ID: "avg"}, {ID: "max"}}},
		{ID: "zookeeper.connections", Title: "Alive Connections", Units: "connections", Priority: 51020,
			Dimensions: []*registry.Dimension{{ID: "alive"}}},
		{ID: "zookeeper.packets", Title: "Packets", Units: "pps", Priority: 51030,
			Dimensions: []*registry.Dimension{incDim("received"), {ID: "sent", Algorithm: inc, Multiplier: -1}}},
		{ID: "zookeeper.file_descriptor", Title: "Open File Descriptors", Units: "file descriptors", Priority: 51035,
			Dimensions: []*registry.Dimension{{ID: "open"}}},
		{ID: "zookeeper.nodes", Title: "Number of Nodes", Units: "nodes", Priority: 51040,
			Dimensions: []*registry.Dimension{{ID: "znode"}, {ID: "ephemerals"}}},
		{ID: "zookeeper.watches", Title: "Number of Watches", Units: "watches", Priority: 51041,
			Dimensions: []*registry.Dimension{{ID: "watches"}}},
		{ID: "zookeeper.approximate_data_size", Title: "Approximate Data Tree Size", Units: "KiB", Priority: 51042,
			Dimensions: []*registry.Dimension{{ID: "size", Divisor: 1024}}},
		{ID: "zookeeper.server_state", Title: "Server State", Units: "state", Type: registry.Stacked, Priority: 51050,
			Dimensions: []*registry.Dimension{{ID: "leader"}, {ID: "follower"}, {ID: "observer"}, {ID: "standalone"}}},
		{ID: "zookeeper.uptime", Title: "Uptime", Units: "seconds", Priority: 51060,
			Dimensions: []*registry.Dimension{{ID: "uptime"}}},
	} {
		c.Family, c.Plugin, c.Module = "zookeeper", "zookeeper", "zookeeper"
		reg.AddChart(c)
	}
	return nil
}

func (z *zookeeperCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := z.mntr(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("zookeeper.requests", now, map[string]float64{"outstanding": firstFloat(m["zk_outstanding_requests"])})
	_ = reg.Collect("zookeeper.requests_latency", now, map[string]float64{
		"min": firstFloat(m["zk_min_latency"]), "avg": firstFloat(m["zk_avg_latency"]), "max": firstFloat(m["zk_max_latency"])})
	_ = reg.Collect("zookeeper.connections", now, map[string]float64{"alive": firstFloat(m["zk_num_alive_connections"])})
	_ = reg.Collect("zookeeper.packets", now, map[string]float64{
		"received": firstFloat(m["zk_packets_received"]), "sent": firstFloat(m["zk_packets_sent"])})
	_ = reg.Collect("zookeeper.file_descriptor", now, map[string]float64{"open": firstFloat(m["zk_open_file_descriptor_count"])})
	_ = reg.Collect("zookeeper.nodes", now, map[string]float64{
		"znode": firstFloat(m["zk_znode_count"]), "ephemerals": firstFloat(m["zk_ephemerals_count"])})
	_ = reg.Collect("zookeeper.watches", now, map[string]float64{"watches": firstFloat(m["zk_watch_count"])})
	_ = reg.Collect("zookeeper.approximate_data_size", now, map[string]float64{"size": firstFloat(m["zk_approximate_data_size"])})
	_ = reg.Collect("zookeeper.server_state", now, zkState(m["zk_server_state"]))
	_ = reg.Collect("zookeeper.uptime", now, map[string]float64{"uptime": firstFloat(m["zk_uptime"])})
	return nil
}

func (z *zookeeperCollector) mntr(ctx context.Context) (map[string]string, error) {
	d := net.Dialer{Timeout: z.cfg.Timeout}
	conn, err := d.DialContext(ctx, "tcp", z.cfg.Address)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(z.cfg.Timeout))
	if _, err := conn.Write([]byte("mntr\n")); err != nil {
		return nil, err
	}
	buf := make([]byte, 64<<10)
	n, err := conn.Read(buf)
	if n == 0 && err != nil {
		return nil, err
	}
	s := string(buf[:n])
	if strings.Contains(s, "not in the whitelist") || strings.Contains(s, "not executed") {
		return nil, fmt.Errorf("zookeeper: mntr disabled")
	}
	m := parseKVTab(s)
	if _, ok := m["zk_server_state"]; !ok && m["zk_znode_count"] == "" && m["zk_num_alive_connections"] == "" {
		return nil, fmt.Errorf("zookeeper: unexpected mntr output")
	}
	return m, nil
}

func zkState(s string) map[string]float64 {
	out := map[string]float64{"leader": 0, "follower": 0, "observer": 0, "standalone": 0}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "leader":
		out["leader"] = 1
	case "follower":
		out["follower"] = 1
	case "observer":
		out["observer"] = 1
	default:
		out["standalone"] = 1
	}
	return out
}
