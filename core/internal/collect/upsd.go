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

// upsdConfig is collectors.modules.upsd (NUT network protocol).
type upsdConfig struct {
	Address  string        `yaml:"address"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type upsdCollector struct {
	cfg  upsdConfig
	dial func(ctx context.Context, network, address string) (net.Conn, error)
	seen map[string]bool
}

func init() {
	Register("upsd", func() Collector { return &upsdCollector{} })
}

func (u *upsdCollector) Name() string { return "upsd" }

func (u *upsdCollector) Configure(decode func(v any) error) error {
	if err := decode(&u.cfg); err != nil {
		return err
	}
	if u.cfg.Address == "" {
		u.cfg.Address = "127.0.0.1:3493"
	}
	if u.cfg.Timeout <= 0 {
		u.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (u *upsdCollector) Init(reg *registry.Registry) error {
	if u.cfg.Address == "" {
		if err := u.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	u.seen = map[string]bool{}
	ups, err := u.list(context.Background())
	if err != nil {
		return err
	}
	if len(ups) == 0 {
		return fmt.Errorf("upsd: no UPS")
	}
	return nil
}

func (u *upsdCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	names, err := u.list(ctx)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return fmt.Errorf("upsd: no UPS")
	}
	for _, name := range names {
		vars, err := u.vars(ctx, name)
		if err != nil {
			return err
		}
		u.ensure(reg, name)
		id := sanitizeID(name)
		status := strings.Fields(vars["ups.status"])
		st := map[string]float64{}
		for _, d := range []string{"on_line", "on_battery", "low_battery", "charging", "overloaded", "other"} {
			st[d] = 0
		}
		got := false
		for _, s := range status {
			switch s {
			case "OL":
				st["on_line"] = 1
				got = true
			case "OB":
				st["on_battery"] = 1
				got = true
			case "LB":
				st["low_battery"] = 1
				got = true
			case "CHRG":
				st["charging"] = 1
				got = true
			case "OVER":
				st["overloaded"] = 1
				got = true
			}
		}
		if !got {
			st["other"] = 1
		}
		_ = reg.Collect("upsd.ups_status."+id, now, st)
		_ = reg.Collect("upsd.ups_load."+id, now, map[string]float64{"load": firstFloat(vars["ups.load"])})
		_ = reg.Collect("upsd.ups_battery_charge."+id, now, map[string]float64{"charge": firstFloat(vars["battery.charge"])})
		_ = reg.Collect("upsd.ups_battery_estimated_runtime."+id, now, map[string]float64{"runtime": firstFloat(vars["battery.runtime"])})
		_ = reg.Collect("upsd.ups_input_voltage."+id, now, map[string]float64{"voltage": firstFloat(vars["input.voltage"])})
	}
	return nil
}

func (u *upsdCollector) ensure(reg *registry.Registry, name string) {
	if u.seen[name] {
		return
	}
	u.seen[name] = true
	id := sanitizeID(name)
	labels := map[string]string{"ups_name": name}
	charts := []*registry.Chart{
		{ID: "upsd.ups_status." + id, Context: "upsd.ups_status", Title: "UPS status", Units: "status", Priority: 55500, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "on_line"}, {ID: "on_battery"}, {ID: "low_battery"}, {ID: "charging"}, {ID: "overloaded"}, {ID: "other"}}},
		{ID: "upsd.ups_load." + id, Context: "upsd.ups_load", Title: "UPS load", Units: "percentage", Type: registry.Area, Priority: 55510, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "load"}}},
		{ID: "upsd.ups_battery_charge." + id, Context: "upsd.ups_battery_charge", Title: "UPS Battery charge", Units: "percentage", Type: registry.Area, Priority: 55520, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "charge"}}},
		{ID: "upsd.ups_battery_estimated_runtime." + id, Context: "upsd.ups_battery_estimated_runtime", Title: "UPS Battery estimated runtime", Units: "seconds", Priority: 55530, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "runtime"}}},
		{ID: "upsd.ups_input_voltage." + id, Context: "upsd.ups_input_voltage", Title: "UPS Input voltage", Units: "Volts", Priority: 55540, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "voltage"}}},
	}
	for _, c := range charts {
		c.Family, c.Plugin, c.Module = "upsd", "upsd", "upsd"
		reg.AddChart(c)
	}
}

func (u *upsdCollector) list(ctx context.Context) ([]string, error) {
	lines, err := u.cmd(ctx, "LIST UPS")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range lines {
		if strings.HasPrefix(line, "UPS ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				names = append(names, fields[1])
			}
		}
	}
	return names, nil
}

func (u *upsdCollector) vars(ctx context.Context, name string) (map[string]string, error) {
	lines, err := u.cmd(ctx, "LIST VAR "+name)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range lines {
		if !strings.HasPrefix(line, "VAR ") {
			continue
		}
		rest := strings.TrimPrefix(line, "VAR ")
		parts := strings.SplitN(rest, " ", 3)
		if len(parts) < 3 {
			continue
		}
		out[parts[1]] = strings.Trim(parts[2], `"`)
	}
	return out, nil
}

func (u *upsdCollector) cmd(ctx context.Context, command string) ([]string, error) {
	dial := u.dial
	if dial == nil {
		nd := net.Dialer{Timeout: u.cfg.Timeout}
		dial = nd.DialContext
	}
	conn, err := dial(ctx, "tcp", u.cfg.Address)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(u.cfg.Timeout))
	if u.cfg.User != "" {
		_, _ = io.WriteString(conn, "USERNAME "+u.cfg.User+"\n")
		_, _ = bufio.NewReader(conn).ReadString('\n')
		_, _ = io.WriteString(conn, "PASSWORD "+u.cfg.Password+"\n")
		_, _ = bufio.NewReader(conn).ReadString('\n')
	}
	if _, err := io.WriteString(conn, command+"\n"); err != nil {
		return nil, err
	}
	br := bufio.NewReader(conn)
	var lines []string
	end := "END " + command
	if i := strings.Index(command, " "); i > 0 {
		end = "END " + command
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			if len(lines) == 0 {
				return nil, err
			}
			break
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ERR ") {
			return nil, fmt.Errorf("upsd: %s", line)
		}
		lines = append(lines, line)
		if strings.HasPrefix(line, end) || strings.HasPrefix(line, "END LIST") {
			break
		}
	}
	return lines, nil
}
