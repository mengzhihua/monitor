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

// dovecotConfig is collectors.modules.dovecot (old_stats EXPORT).
type dovecotConfig struct {
	Address string        `yaml:"address"`
	Timeout time.Duration `yaml:"timeout"`
}

type dovecotCollector struct {
	cfg  dovecotConfig
	dial func(ctx context.Context, network, address string) (net.Conn, error)
}

func init() {
	Register("dovecot", func() Collector { return &dovecotCollector{} })
}

func (d *dovecotCollector) Name() string { return "dovecot" }

func (d *dovecotCollector) Configure(decode func(v any) error) error {
	if err := decode(&d.cfg); err != nil {
		return err
	}
	if d.cfg.Address == "" {
		d.cfg.Address = "127.0.0.1:24242"
	}
	if d.cfg.Timeout <= 0 {
		d.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (d *dovecotCollector) Init(reg *registry.Registry) error {
	if d.cfg.Address == "" {
		if err := d.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := d.export(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "dovecot.sessions", Title: "Dovecot Active Sessions", Units: "sessions", Priority: 52200,
			Dimensions: []*registry.Dimension{{ID: "active"}}},
		{ID: "dovecot.logins", Title: "Dovecot Logins", Units: "logins", Priority: 52210,
			Dimensions: []*registry.Dimension{{ID: "logins"}}},
		{ID: "dovecot.auth", Title: "Dovecot Authentications", Units: "attempts/s", Type: registry.Stacked, Priority: 52220,
			Dimensions: []*registry.Dimension{{ID: "ok", Algorithm: inc}, {ID: "failed", Algorithm: inc}}},
		{ID: "dovecot.commands", Title: "Dovecot Commands", Units: "commands", Priority: 52230,
			Dimensions: []*registry.Dimension{{ID: "commands"}}},
		{ID: "dovecot.faults", Title: "Dovecot Page Faults", Units: "faults/s", Priority: 52240,
			Dimensions: []*registry.Dimension{{ID: "minor", Algorithm: inc}, {ID: "major", Algorithm: inc}}},
		{ID: "dovecot.io", Title: "Dovecot Disk I/O", Units: "KiB/s", Type: registry.Area, Priority: 52250,
			Dimensions: []*registry.Dimension{
				{ID: "read", Algorithm: inc, Divisor: 1024}, {ID: "write", Algorithm: inc, Multiplier: -1, Divisor: 1024}}},
		{ID: "dovecot.net", Title: "Dovecot Network Bandwidth", Units: "kilobits/s", Type: registry.Area, Priority: 52260,
			Dimensions: []*registry.Dimension{
				{ID: "read", Algorithm: inc, Multiplier: 8, Divisor: 1000},
				{ID: "write", Algorithm: inc, Multiplier: -8, Divisor: 1000}}},
		{ID: "dovecot.cache", Title: "Dovecot Cache Hits", Units: "hits/s", Priority: 52270,
			Dimensions: []*registry.Dimension{{ID: "hits", Algorithm: inc}}},
		{ID: "dovecot.auth_cache", Title: "Dovecot Authentication Cache", Units: "requests/s", Type: registry.Stacked, Priority: 52280,
			Dimensions: []*registry.Dimension{{ID: "hits", Algorithm: inc}, {ID: "misses", Algorithm: inc}}},
	} {
		c.Family, c.Plugin, c.Module = "dovecot", "dovecot", "dovecot"
		reg.AddChart(c)
	}
	return nil
}

func (d *dovecotCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := d.export(ctx)
	if err != nil {
		return err
	}
	g := func(k string) float64 { return m[k] }
	_ = reg.Collect("dovecot.sessions", now, map[string]float64{"active": g("num_connected_sessions")})
	_ = reg.Collect("dovecot.logins", now, map[string]float64{"logins": g("num_logins")})
	_ = reg.Collect("dovecot.auth", now, map[string]float64{"ok": g("auth_successes"), "failed": g("auth_failures")})
	_ = reg.Collect("dovecot.commands", now, map[string]float64{"commands": g("num_cmds")})
	_ = reg.Collect("dovecot.faults", now, map[string]float64{"minor": g("min_faults"), "major": g("maj_faults")})
	_ = reg.Collect("dovecot.io", now, map[string]float64{"read": g("disk_input"), "write": g("disk_output")})
	_ = reg.Collect("dovecot.net", now, map[string]float64{"read": g("read_bytes"), "write": g("write_bytes")})
	_ = reg.Collect("dovecot.cache", now, map[string]float64{"hits": g("mail_cache_hits")})
	_ = reg.Collect("dovecot.auth_cache", now, map[string]float64{"hits": g("auth_cache_hits"), "misses": g("auth_cache_misses")})
	return nil
}

func (d *dovecotCollector) export(ctx context.Context) (map[string]float64, error) {
	network, addr := "tcp", d.cfg.Address
	if strings.HasPrefix(addr, "/") {
		network = "unix"
	}
	dial := d.dial
	if dial == nil {
		nd := net.Dialer{Timeout: d.cfg.Timeout}
		dial = nd.DialContext
	}
	conn, err := dial(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(d.cfg.Timeout))
	if _, err := io.WriteString(conn, "EXPORT\tglobal\n"); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(conn, 1<<20))
	if err != nil && len(body) == 0 {
		return nil, err
	}
	m, err := parseDovecotExport(string(body))
	if err != nil {
		return nil, err
	}
	return m, nil
}

func parseDovecotExport(s string) (map[string]float64, error) {
	sc := bufio.NewScanner(strings.NewReader(s))
	var fields, values []string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		cols := strings.Fields(line)
		if fields == nil {
			fields = cols
			continue
		}
		values = cols
		break
	}
	if len(fields) == 0 || len(values) == 0 {
		return nil, fmt.Errorf("dovecot: empty EXPORT")
	}
	if len(values) < len(fields) {
		return nil, fmt.Errorf("dovecot: mismatched EXPORT fields")
	}
	out := map[string]float64{}
	for i, name := range fields {
		out[name] = firstFloat(values[i])
	}
	if _, ok := out["num_logins"]; !ok && out["num_connected_sessions"] == 0 && out["num_cmds"] == 0 {
		return nil, fmt.Errorf("dovecot: unexpected EXPORT")
	}
	return out, nil
}
