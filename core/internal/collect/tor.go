package collect

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// torConfig is collectors.modules.tor (control port GETINFO).
type torConfig struct {
	Address  string        `yaml:"address"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type torCollector struct {
	cfg  torConfig
	dial func(ctx context.Context, network, address string) (net.Conn, error)
}

func init() {
	Register("tor", func() Collector { return &torCollector{} })
}

func (t *torCollector) Name() string { return "tor" }

func (t *torCollector) Configure(decode func(v any) error) error {
	if err := decode(&t.cfg); err != nil {
		return err
	}
	if t.cfg.Address == "" {
		t.cfg.Address = "127.0.0.1:9051"
	}
	if t.cfg.Timeout <= 0 {
		t.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (t *torCollector) Init(reg *registry.Registry) error {
	if t.cfg.Address == "" {
		if err := t.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := t.info(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "tor.traffic", Title: "Tor Traffic", Units: "KiB/s", Type: registry.Area, Priority: 53000,
			Dimensions: []*registry.Dimension{
				{ID: "read", Algorithm: inc, Divisor: 1024},
				{ID: "write", Algorithm: inc, Multiplier: -1, Divisor: 1024}}},
		{ID: "tor.uptime", Title: "Tor Uptime", Units: "seconds", Priority: 53010,
			Dimensions: []*registry.Dimension{{ID: "uptime"}}},
	} {
		c.Family, c.Plugin, c.Module = "tor", "tor", "tor"
		reg.AddChart(c)
	}
	return nil
}

func (t *torCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := t.info(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("tor.traffic", now, map[string]float64{"read": m["traffic/read"], "write": m["traffic/written"]})
	_ = reg.Collect("tor.uptime", now, map[string]float64{"uptime": m["uptime"]})
	return nil
}

func (t *torCollector) info(ctx context.Context) (map[string]float64, error) {
	dial := t.dial
	if dial == nil {
		nd := net.Dialer{Timeout: t.cfg.Timeout}
		dial = nd.DialContext
	}
	conn, err := dial(ctx, "tcp", t.cfg.Address)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(t.cfg.Timeout))
	auth := "AUTHENTICATE\n"
	if t.cfg.Password != "" {
		auth = fmt.Sprintf("AUTHENTICATE %q\n", t.cfg.Password)
	}
	if _, err := io.WriteString(conn, auth); err != nil {
		return nil, err
	}
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(line, "250") {
		return nil, fmt.Errorf("tor: auth failed: %s", strings.TrimSpace(line))
	}
	if _, err := io.WriteString(conn, "GETINFO traffic/read traffic/written uptime\n"); err != nil {
		return nil, err
	}
	out := map[string]float64{}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			if len(out) == 0 {
				return nil, err
			}
			break
		}
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "250-"):
			kv := strings.TrimPrefix(line, "250-")
			k, v, ok := strings.Cut(kv, "=")
			if ok {
				out[strings.TrimSpace(k)] = firstFloat(v)
			}
		case strings.HasPrefix(line, "250 "):
			return out, nil
		default:
			if line != "" && !strings.HasPrefix(line, "250") {
				return nil, fmt.Errorf("tor: %s", line)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("tor: empty GETINFO")
	}
	return out, nil
}
