package collect

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

type memcachedConfig struct {
	Address string        `yaml:"address"` // host:port or unix:///path
	Timeout time.Duration `yaml:"timeout"`
}

type memcachedCollector struct {
	cfg memcachedConfig
}

func init() {
	Register("memcached", func() Collector { return &memcachedCollector{} })
}

func (m *memcachedCollector) Name() string { return "memcached" }

func (m *memcachedCollector) Configure(decode func(v any) error) error {
	if err := decode(&m.cfg); err != nil {
		return err
	}
	if m.cfg.Address == "" {
		m.cfg.Address = "127.0.0.1:11211"
	}
	if m.cfg.Timeout <= 0 {
		m.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (m *memcachedCollector) Init(reg *registry.Registry) error {
	if m.cfg.Address == "" {
		if err := m.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := m.stats(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "memcached.cache", Title: "Memcached cache size", Units: "MiB", Type: registry.Stacked, Priority: 44000,
			Dimensions: []*registry.Dimension{{ID: "used", Divisor: 1 << 20}, {ID: "free", Divisor: 1 << 20}}},
		{ID: "memcached.net", Title: "Memcached network", Units: "kilobits/s", Type: registry.Area, Priority: 44010,
			Dimensions: []*registry.Dimension{
				{ID: "in", Algorithm: inc, Multiplier: 8, Divisor: 1000},
				{ID: "out", Algorithm: inc, Multiplier: -8, Divisor: 1000}}},
		{ID: "memcached.connections", Title: "Memcached connections", Units: "connections", Priority: 44020,
			Dimensions: []*registry.Dimension{{ID: "current"}, {ID: "total", Algorithm: inc, Hidden: true}}},
		{ID: "memcached.items", Title: "Memcached items", Units: "items", Priority: 44030,
			Dimensions: []*registry.Dimension{{ID: "current"}, {ID: "evicted", Algorithm: inc}}},
		{ID: "memcached.get_hits", Title: "Memcached GET hits/misses", Units: "requests/s", Type: registry.Stacked, Priority: 44040,
			Dimensions: []*registry.Dimension{{ID: "hits", Algorithm: inc}, {ID: "misses", Algorithm: inc}}},
		{ID: "memcached.ops", Title: "Memcached operations", Units: "operations/s", Priority: 44050,
			Dimensions: []*registry.Dimension{
				{ID: "get", Algorithm: inc}, {ID: "set", Algorithm: inc},
				{ID: "delete", Algorithm: inc}, {ID: "cas", Algorithm: inc}}},
	} {
		c.Family, c.Plugin, c.Module = "memcached", "memcached", "memcached"
		reg.AddChart(c)
	}
	return nil
}

func (m *memcachedCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	s, err := m.stats(ctx)
	if err != nil {
		return err
	}
	limit := num(s, "limit_maxbytes")
	used := num(s, "bytes")
	free := limit - used
	if free < 0 {
		free = 0
	}
	_ = reg.Collect("memcached.cache", now, map[string]float64{"used": used, "free": free})
	_ = reg.Collect("memcached.net", now, map[string]float64{"in": num(s, "bytes_read"), "out": num(s, "bytes_written")})
	_ = reg.Collect("memcached.connections", now, map[string]float64{"current": num(s, "curr_connections"), "total": num(s, "total_connections")})
	_ = reg.Collect("memcached.items", now, map[string]float64{"current": num(s, "curr_items"), "evicted": num(s, "evictions")})
	_ = reg.Collect("memcached.get_hits", now, map[string]float64{"hits": num(s, "get_hits"), "misses": num(s, "get_misses")})
	_ = reg.Collect("memcached.ops", now, map[string]float64{
		"get": num(s, "cmd_get"), "set": num(s, "cmd_set"),
		"delete": num(s, "delete_hits") + num(s, "delete_misses"), "cas": num(s, "cas_hits") + num(s, "cas_misses")})
	return nil
}

func (m *memcachedCollector) stats(ctx context.Context) (map[string]string, error) {
	d := net.Dialer{Timeout: m.cfg.Timeout}
	network, addr := "tcp", m.cfg.Address
	if strings.HasPrefix(addr, "unix://") {
		network, addr = "unix", strings.TrimPrefix(addr, "unix://")
	}
	conn, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(m.cfg.Timeout))
	if _, err := conn.Write([]byte("stats\r\n")); err != nil {
		return nil, err
	}
	br := bufio.NewReader(conn)
	out := map[string]string{}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "END" {
			return out, nil
		}
		if strings.HasPrefix(line, "STAT ") {
			f := strings.SplitN(line, " ", 3)
			if len(f) == 3 {
				out[f[1]] = f[2]
			}
		}
		if strings.HasPrefix(line, "ERROR") {
			return nil, fmt.Errorf("memcached: %s", line)
		}
	}
}

func num(m map[string]string, k string) float64 {
	n, _ := strconv.ParseFloat(m[k], 64)
	return n
}
