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

// openvpnConfig is collectors.modules.openvpn (management interface).
type openvpnConfig struct {
	Address  string        `yaml:"address"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type openvpnCollector struct {
	cfg  openvpnConfig
	dial func(ctx context.Context, network, address string) (net.Conn, error)
}

func init() {
	Register("openvpn", func() Collector { return &openvpnCollector{} })
}

func (o *openvpnCollector) Name() string { return "openvpn" }

func (o *openvpnCollector) Configure(decode func(v any) error) error {
	if err := decode(&o.cfg); err != nil {
		return err
	}
	if o.cfg.Address == "" {
		o.cfg.Address = "127.0.0.1:7505"
	}
	if o.cfg.Timeout <= 0 {
		o.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (o *openvpnCollector) Init(reg *registry.Registry) error {
	if o.cfg.Address == "" {
		if err := o.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, _, _, err := o.loadStats(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "openvpn.active_clients", Title: "Total Number Of Active Clients", Units: "clients", Priority: 56700,
			Dimensions: []*registry.Dimension{{ID: "clients"}}},
		{ID: "openvpn.total_traffic", Title: "Total Traffic", Units: "kilobits/s", Type: registry.Area, Priority: 56710,
			Dimensions: []*registry.Dimension{
				{ID: "in", Algorithm: inc, Multiplier: 8, Divisor: 1000},
				{ID: "out", Algorithm: inc, Multiplier: 8, Divisor: -1000},
			}},
	} {
		ch.Family, ch.Plugin, ch.Module = "openvpn", "openvpn", "openvpn"
		reg.AddChart(ch)
	}
	return nil
}

func (o *openvpnCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	n, in, out, err := o.loadStats(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("openvpn.active_clients", now, map[string]float64{"clients": n})
	_ = reg.Collect("openvpn.total_traffic", now, map[string]float64{"in": in, "out": out})
	return nil
}

func (o *openvpnCollector) loadStats(ctx context.Context) (clients, bytesIn, bytesOut float64, err error) {
	dial := o.dial
	if dial == nil {
		d := net.Dialer{Timeout: o.cfg.Timeout}
		dial = d.DialContext
	}
	conn, err := dial(ctx, "tcp", o.cfg.Address)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("openvpn: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(o.cfg.Timeout))
	r := bufio.NewReader(conn)
	// banner or ENTER PASSWORD
	line, _ := r.ReadString('\n')
	if strings.Contains(strings.ToUpper(line), "PASSWORD") {
		if o.cfg.Password == "" {
			return 0, 0, 0, fmt.Errorf("openvpn: password required")
		}
		if _, err := fmt.Fprintf(conn, "%s\n", o.cfg.Password); err != nil {
			return 0, 0, 0, err
		}
		_, _ = r.ReadString('\n')
	}
	if _, err := fmt.Fprintf(conn, "load-stats\n"); err != nil {
		return 0, 0, 0, err
	}
	resp, err := r.ReadString('\n')
	if err != nil {
		return 0, 0, 0, fmt.Errorf("openvpn: %w", err)
	}
	// SUCCESS: nclients=2,bytesin=100,bytesout=200
	resp = strings.TrimSpace(resp)
	if i := strings.Index(resp, "nclients="); i >= 0 {
		resp = resp[i:]
	}
	for _, part := range strings.Split(resp, ",") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "nclients":
			clients = firstFloat(v)
		case "bytesin":
			bytesIn = firstFloat(v)
		case "bytesout":
			bytesOut = firstFloat(v)
		}
	}
	_, _ = fmt.Fprintf(conn, "quit\n")
	return clients, bytesIn, bytesOut, nil
}
