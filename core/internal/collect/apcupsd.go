package collect

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// apcupsdConfig is collectors.modules.apcupsd (Netdata go.d apcupsd).
type apcupsdConfig struct {
	Address string        `yaml:"address"` // default 127.0.0.1:3551
	Timeout time.Duration `yaml:"timeout"`
}

type apcupsdCollector struct {
	cfg  apcupsdConfig
	dial func(ctx context.Context) (net.Conn, error)
}

func init() {
	Register("apcupsd", func() Collector { return &apcupsdCollector{} })
}

func (a *apcupsdCollector) Name() string { return "apcupsd" }

func (a *apcupsdCollector) Configure(decode func(v any) error) error {
	if err := decode(&a.cfg); err != nil {
		return err
	}
	if a.cfg.Address == "" {
		a.cfg.Address = "127.0.0.1:3551"
	}
	if a.cfg.Timeout <= 0 {
		a.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (a *apcupsdCollector) Init(reg *registry.Registry) error {
	if a.cfg.Address == "" {
		if err := a.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := a.status(context.Background()); err != nil {
		return err
	}
	for _, c := range []*registry.Chart{
		{ID: "apcupsd.ups_status", Title: "UPS status", Units: "status", Type: registry.Stacked, Priority: 49200,
			Dimensions: []*registry.Dimension{
				{ID: "online"}, {ID: "onbattery"}, {ID: "charging"}, {ID: "overload"}, {ID: "commlost"}}},
		{ID: "apcupsd.ups_battery_charge", Title: "UPS battery charge", Units: "percent", Type: registry.Area, Priority: 49210,
			Dimensions: []*registry.Dimension{{ID: "charge"}}},
		{ID: "apcupsd.ups_battery_voltage", Title: "UPS battery voltage", Units: "Volts", Priority: 49220,
			Dimensions: []*registry.Dimension{{ID: "voltage"}}},
		{ID: "apcupsd.ups_load_capacity_utilization", Title: "UPS load capacity", Units: "percent", Priority: 49230,
			Dimensions: []*registry.Dimension{{ID: "load"}}},
		{ID: "apcupsd.ups_input_voltage", Title: "UPS input voltage", Units: "Volts", Priority: 49240,
			Dimensions: []*registry.Dimension{{ID: "voltage"}}},
		{ID: "apcupsd.ups_battery_time_remaining", Title: "UPS time remaining", Units: "seconds", Priority: 49250,
			Dimensions: []*registry.Dimension{{ID: "timeleft"}}},
		{ID: "apcupsd.ups_temperature", Title: "UPS temperature", Units: "Celsius", Priority: 49260,
			Dimensions: []*registry.Dimension{{ID: "temperature"}}},
	} {
		c.Family, c.Plugin, c.Module = "apcupsd", "apcupsd", "apcupsd"
		reg.AddChart(c)
	}
	return nil
}

func (a *apcupsdCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	st, err := a.status(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("apcupsd.ups_status", now, apcStatusDims(st["STATUS"]))
	_ = reg.Collect("apcupsd.ups_battery_charge", now, map[string]float64{"charge": apcNum(st["BCHARGE"])})
	_ = reg.Collect("apcupsd.ups_battery_voltage", now, map[string]float64{"voltage": apcNum(st["BATTV"])})
	_ = reg.Collect("apcupsd.ups_load_capacity_utilization", now, map[string]float64{"load": apcNum(st["LOADPCT"])})
	_ = reg.Collect("apcupsd.ups_input_voltage", now, map[string]float64{"voltage": apcNum(st["LINEV"])})
	_ = reg.Collect("apcupsd.ups_battery_time_remaining", now, map[string]float64{"timeleft": apcMinutes(st["TIMELEFT"])})
	_ = reg.Collect("apcupsd.ups_temperature", now, map[string]float64{"temperature": apcNum(st["ITEMP"])})
	return nil
}

func (a *apcupsdCollector) status(ctx context.Context) (map[string]string, error) {
	dial := a.dial
	if dial == nil {
		d := net.Dialer{Timeout: a.cfg.Timeout}
		dial = func(ctx context.Context) (net.Conn, error) {
			return d.DialContext(ctx, "tcp", a.cfg.Address)
		}
	}
	conn, err := dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(a.cfg.Timeout))
	if err := apcWrite(conn, "status"); err != nil {
		return nil, err
	}
	raw, err := apcReadAll(conn)
	if err != nil {
		return nil, err
	}
	st := parseAPCStatus(raw)
	if len(st) == 0 {
		return nil, fmt.Errorf("apcupsd: empty status")
	}
	return st, nil
}

func apcWrite(w io.Writer, s string) error {
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(s)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := io.WriteString(w, s)
	return err
}

func apcReadAll(r io.Reader) (string, error) {
	var b strings.Builder
	for {
		var hdr [2]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if b.Len() > 0 {
				break
			}
			return "", err
		}
		n := int(binary.BigEndian.Uint16(hdr[:]))
		if n == 0 {
			break
		}
		if n > 1024 {
			return "", fmt.Errorf("apcupsd: line too long")
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		b.Write(buf)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func parseAPCStatus(s string) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

func apcNum(s string) float64 {
	return firstFloat(s)
}

func apcMinutes(s string) float64 {
	// "12.5 Minutes" → seconds
	return firstFloat(s) * 60
}

func apcStatusDims(status string) map[string]float64 {
	out := map[string]float64{"online": 0, "onbattery": 0, "charging": 0, "overload": 0, "commlost": 0}
	u := strings.ToUpper(status)
	switch {
	case strings.Contains(u, "COMMLOST"):
		out["commlost"] = 1
	case strings.Contains(u, "OVERLOAD"):
		out["overload"] = 1
	case strings.Contains(u, "ONBATT"):
		out["onbattery"] = 1
	case strings.Contains(u, "CHARGING"):
		out["charging"] = 1
	default:
		out["online"] = 1
	}
	if strings.Contains(u, "ONLINE") {
		out["online"] = 1
	}
	return out
}
