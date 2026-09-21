package collect

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// redisConfig is the `collectors.modules.redis` section. A tiny RESP client
// (no third-party driver) issues AUTH/INFO over a fresh connection per cycle.
type redisConfig struct {
	Address  string        `yaml:"address"` // host:port or unix:///path (default 127.0.0.1:6379)
	Username string        `yaml:"username"`
	Password string        `yaml:"password"`
	TLS      bool          `yaml:"tls"`
	Timeout  time.Duration `yaml:"timeout"`
}

type redisCollector struct {
	cfg redisConfig
}

func init() {
	Register("redis", func() Collector { return &redisCollector{} })
}

func (r *redisCollector) Name() string { return "redis" }

func (r *redisCollector) Configure(decode func(v any) error) error {
	if err := decode(&r.cfg); err != nil {
		return err
	}
	if r.cfg.Address == "" {
		r.cfg.Address = "127.0.0.1:6379"
	}
	if r.cfg.Timeout <= 0 {
		r.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (r *redisCollector) Init(reg *registry.Registry) error {
	if r.cfg.Address == "" {
		if err := r.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := r.info(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	dims := func(ds ...*registry.Dimension) []*registry.Dimension { return ds }
	charts := []*registry.Chart{
		{ID: "redis.connections", Title: "Redis connections", Units: "connections/s", Priority: 41000,
			Dimensions: dims(&registry.Dimension{ID: "accepted", Algorithm: inc}, &registry.Dimension{ID: "rejected", Algorithm: inc})},
		{ID: "redis.clients", Title: "Redis connected clients", Units: "clients", Priority: 41010,
			Dimensions: dims(&registry.Dimension{ID: "connected"}, &registry.Dimension{ID: "blocked"})},
		{ID: "redis.commands", Title: "Redis processed commands", Units: "commands/s", Priority: 41020,
			Dimensions: dims(&registry.Dimension{ID: "processed", Algorithm: inc})},
		{ID: "redis.keyspace", Title: "Redis keyspace lookups", Units: "lookups/s", Priority: 41030, Type: registry.Stacked,
			Dimensions: dims(&registry.Dimension{ID: "hits", Algorithm: inc}, &registry.Dimension{ID: "misses", Algorithm: inc})},
		{ID: "redis.memory", Title: "Redis memory", Units: "MiB", Priority: 41040, Type: registry.Area,
			Dimensions: dims(&registry.Dimension{ID: "used", Divisor: 1 << 20}, &registry.Dimension{ID: "rss", Divisor: 1 << 20}, &registry.Dimension{ID: "peak", Divisor: 1 << 20})},
		{ID: "redis.net", Title: "Redis network traffic", Units: "kilobits/s", Priority: 41050, Type: registry.Area,
			Dimensions: dims(&registry.Dimension{ID: "received", Algorithm: inc, Multiplier: 8, Divisor: 1000}, &registry.Dimension{ID: "sent", Algorithm: inc, Multiplier: -8, Divisor: 1000})},
		{ID: "redis.keys", Title: "Redis keys", Units: "keys", Priority: 41060, Type: registry.Stacked},
		{ID: "redis.evicted_expired", Title: "Redis evicted/expired keys", Units: "keys/s", Priority: 41070,
			Dimensions: dims(&registry.Dimension{ID: "evicted", Algorithm: inc}, &registry.Dimension{ID: "expired", Algorithm: inc})},
		{ID: "redis.uptime", Title: "Redis uptime", Units: "seconds", Priority: 41080,
			Dimensions: dims(&registry.Dimension{ID: "uptime"})},
	}
	for _, c := range charts {
		c.Family, c.Plugin, c.Module = "redis", "redis", "redis"
		reg.AddChart(c)
	}
	return nil
}

func (r *redisCollector) dial(ctx context.Context) (net.Conn, error) {
	d := &net.Dialer{Timeout: r.cfg.Timeout}
	network, addr := "tcp", r.cfg.Address
	if strings.HasPrefix(addr, "unix://") {
		network, addr = "unix", strings.TrimPrefix(addr, "unix://")
	}
	conn, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	if r.cfg.TLS {
		host, _, _ := net.SplitHostPort(addr)
		tc := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		conn = tc
	}
	return conn, nil
}

func respCommand(args ...string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	return []byte(b.String())
}

// respRead parses one RESP2 reply: simple string, error, integer or bulk string.
func respRead(br *bufio.Reader) (string, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", errors.New("redis: empty reply")
	}
	switch line[0] {
	case '+', ':':
		return line[1:], nil
	case '-':
		return "", fmt.Errorf("redis: %s", line[1:])
	case '$':
		n, err := strconv.Atoi(line[1:])
		if err != nil || n < 0 {
			return "", nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(br, buf); err != nil {
			return "", err
		}
		return string(buf[:n]), nil
	}
	return "", fmt.Errorf("redis: unexpected reply %q", line)
}

func (r *redisCollector) info(ctx context.Context) (map[string]string, error) {
	conn, err := r.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(r.cfg.Timeout))
	}
	br := bufio.NewReader(conn)
	if r.cfg.Password != "" {
		args := []string{"AUTH", r.cfg.Password}
		if r.cfg.Username != "" {
			args = []string{"AUTH", r.cfg.Username, r.cfg.Password}
		}
		if _, err := conn.Write(respCommand(args...)); err != nil {
			return nil, err
		}
		if _, err := respRead(br); err != nil {
			return nil, err
		}
	}
	if _, err := conn.Write(respCommand("INFO")); err != nil {
		return nil, err
	}
	body, err := respRead(br)
	if err != nil {
		return nil, err
	}
	return parseRedisInfo(body), nil
}

// parseRedisInfo turns "key:value" lines into a map; "# Section" lines are skipped.
func parseRedisInfo(body string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			out[k] = v
		}
	}
	return out
}

func infoNum(m map[string]string, key string) float64 {
	n, _ := strconv.ParseFloat(m[key], 64)
	return n
}

func (r *redisCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := r.info(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("redis.connections", now, map[string]float64{"accepted": infoNum(m, "total_connections_received"), "rejected": infoNum(m, "rejected_connections")})
	_ = reg.Collect("redis.clients", now, map[string]float64{"connected": infoNum(m, "connected_clients"), "blocked": infoNum(m, "blocked_clients")})
	_ = reg.Collect("redis.commands", now, map[string]float64{"processed": infoNum(m, "total_commands_processed")})
	_ = reg.Collect("redis.keyspace", now, map[string]float64{"hits": infoNum(m, "keyspace_hits"), "misses": infoNum(m, "keyspace_misses")})
	_ = reg.Collect("redis.memory", now, map[string]float64{"used": infoNum(m, "used_memory"), "rss": infoNum(m, "used_memory_rss"), "peak": infoNum(m, "used_memory_peak")})
	_ = reg.Collect("redis.net", now, map[string]float64{"received": infoNum(m, "total_net_input_bytes"), "sent": infoNum(m, "total_net_output_bytes")})
	_ = reg.Collect("redis.evicted_expired", now, map[string]float64{"evicted": infoNum(m, "evicted_keys"), "expired": infoNum(m, "expired_keys")})
	_ = reg.Collect("redis.uptime", now, map[string]float64{"uptime": infoNum(m, "uptime_in_seconds")})

	// db0:keys=12,expires=0,avg_ttl=0 → one dimension per database.
	keys := map[string]float64{}
	if ch, ok := reg.Chart("redis.keys"); ok {
		for _, d := range ch.Dims() { // databases absent from INFO are empty
			keys[d.ID] = 0
		}
		for k, v := range m {
			if !strings.HasPrefix(k, "db") {
				continue
			}
			for _, kv := range strings.Split(v, ",") {
				if name, val, ok := strings.Cut(kv, "="); ok && name == "keys" {
					ch.AddDimension(&registry.Dimension{ID: k})
					keys[k], _ = strconv.ParseFloat(val, 64)
				}
			}
		}
	}
	_ = reg.Collect("redis.keys", now, keys)
	return nil
}
