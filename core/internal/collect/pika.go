package collect

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// pikaConfig is collectors.modules.pika (Redis-protocol INFO :9221).
type pikaConfig struct {
	Address  string        `yaml:"address"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type pikaCollector struct {
	cfg  pikaConfig
	dial func(ctx context.Context, network, address string) (net.Conn, error)
}

func init() {
	Register("pika", func() Collector { return &pikaCollector{} })
}

func (p *pikaCollector) Name() string { return "pika" }

func (p *pikaCollector) Configure(decode func(v any) error) error {
	if err := decode(&p.cfg); err != nil {
		return err
	}
	if p.cfg.Address == "" {
		p.cfg.Address = "127.0.0.1:9221"
	}
	p.cfg.Address = strings.TrimPrefix(strings.TrimPrefix(p.cfg.Address, "redis://"), "@")
	if i := strings.LastIndex(p.cfg.Address, "@"); i >= 0 {
		p.cfg.Address = p.cfg.Address[i+1:]
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (p *pikaCollector) Init(reg *registry.Registry) error {
	if p.cfg.Address == "" {
		if err := p.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := p.info(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "pika.connections", Title: "Connections", Units: "connections/s", Priority: 59800,
			Dimensions: []*registry.Dimension{{ID: "accepted", Algorithm: inc}}},
		{ID: "pika.clients", Title: "Clients", Units: "clients", Priority: 59810,
			Dimensions: []*registry.Dimension{{ID: "connected"}}},
		{ID: "pika.memory", Title: "Memory usage", Units: "bytes", Type: registry.Area, Priority: 59820,
			Dimensions: []*registry.Dimension{{ID: "used"}}},
		{ID: "pika.commands", Title: "Processed commands", Units: "commands/s", Priority: 59830,
			Dimensions: []*registry.Dimension{{ID: "processed", Algorithm: inc}}},
		{ID: "pika.connected_replicas", Title: "Connected replicas", Units: "replicas", Priority: 59840,
			Dimensions: []*registry.Dimension{{ID: "connected"}}},
		{ID: "pika.uptime", Title: "Uptime", Units: "seconds", Priority: 59850,
			Dimensions: []*registry.Dimension{{ID: "uptime"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "pika", "pika", "pika"
		reg.AddChart(ch)
	}
	return nil
}

func (p *pikaCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	info, err := p.info(ctx)
	if err != nil {
		return err
	}
	g := func(k string) float64 { return firstFloat(info[k]) }
	_ = reg.Collect("pika.connections", now, map[string]float64{"accepted": g("total_connections_received")})
	_ = reg.Collect("pika.clients", now, map[string]float64{"connected": g("connected_clients")})
	_ = reg.Collect("pika.memory", now, map[string]float64{"used": g("used_memory")})
	_ = reg.Collect("pika.commands", now, map[string]float64{"processed": g("total_commands_processed")})
	_ = reg.Collect("pika.connected_replicas", now, map[string]float64{"connected": g("connected_slaves")})
	_ = reg.Collect("pika.uptime", now, map[string]float64{"uptime": g("uptime_in_seconds")})
	return nil
}

func (p *pikaCollector) info(ctx context.Context) (map[string]string, error) {
	dial := p.dial
	if dial == nil {
		d := net.Dialer{Timeout: p.cfg.Timeout}
		dial = d.DialContext
	}
	conn, err := dial(ctx, "tcp", p.cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("pika: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(p.cfg.Timeout))
	br := bufio.NewReader(conn)
	if p.cfg.Password != "" {
		if _, err := conn.Write(respCommand("AUTH", p.cfg.Password)); err != nil {
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
		return nil, fmt.Errorf("pika: %w", err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if ok {
			out[k] = v
		}
	}
	if _, ok := out["connected_clients"]; !ok && out["used_memory"] == "" {
		return nil, fmt.Errorf("pika: no info")
	}
	return out, nil
}
