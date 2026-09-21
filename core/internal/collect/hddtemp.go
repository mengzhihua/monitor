package collect

import (
	"context"
	"fmt"
	"io"
	"net"
	"path"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// hddtempConfig is collectors.modules.hddtemp (daemon :7634).
type hddtempConfig struct {
	Address string        `yaml:"address"`
	Timeout time.Duration `yaml:"timeout"`
}

type hddtempCollector struct {
	cfg  hddtempConfig
	dial func(ctx context.Context, network, address string) (net.Conn, error)
	seen map[string]bool
}

func init() {
	Register("hddtemp", func() Collector { return &hddtempCollector{} })
}

func (h *hddtempCollector) Name() string { return "hddtemp" }

func (h *hddtempCollector) Configure(decode func(v any) error) error {
	if err := decode(&h.cfg); err != nil {
		return err
	}
	if h.cfg.Address == "" {
		h.cfg.Address = "127.0.0.1:7634"
	}
	if h.cfg.Timeout <= 0 {
		h.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (h *hddtempCollector) Init(reg *registry.Registry) error {
	if h.cfg.Address == "" {
		if err := h.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	h.seen = map[string]bool{}
	msg, err := h.read(context.Background())
	if err != nil {
		return err
	}
	disks, err := parseHddTemp(msg)
	if err != nil {
		return err
	}
	if len(disks) == 0 {
		return fmt.Errorf("hddtemp: no disks")
	}
	return nil
}

func (h *hddtempCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	msg, err := h.read(ctx)
	if err != nil {
		return err
	}
	disks, err := parseHddTemp(msg)
	if err != nil {
		return err
	}
	for _, d := range disks {
		id := path.Base(d.dev)
		if id == "" || id == "." {
			continue
		}
		sid := sanitizeID(id)
		h.ensure(reg, sid, id)
		st := map[string]float64{"ok": 0, "err": 0, "na": 0, "unk": 0, "nos": 0, "slp": 0}
		temp := 0.0
		switch strings.ToUpper(d.temp) {
		case "NA":
			st["na"] = 1
		case "UNK":
			st["unk"] = 1
		case "NOS":
			st["nos"] = 1
		case "SLP":
			st["slp"] = 1
		case "ERR":
			st["err"] = 1
		default:
			st["ok"] = 1
			temp = firstFloat(d.temp)
			if strings.EqualFold(d.unit, "F") {
				temp = (temp - 32) * 5 / 9
			}
			_ = reg.Collect("hddtemp.disk_temperature."+sid, now, map[string]float64{"temperature": temp})
		}
		_ = reg.Collect("hddtemp.disk_temperature_sensor_status."+sid, now, st)
	}
	return nil
}

func (h *hddtempCollector) ensure(reg *registry.Registry, sid, name string) {
	if h.seen[sid] {
		return
	}
	h.seen[sid] = true
	for _, ch := range []*registry.Chart{
		{ID: "hddtemp.disk_temperature." + sid, Context: "hddtemp.disk_temperature", Title: "Disk temperature", Units: "Celsius", Priority: 56600,
			Dimensions: []*registry.Dimension{{ID: "temperature"}}},
		{ID: "hddtemp.disk_temperature_sensor_status." + sid, Context: "hddtemp.disk_temperature_sensor_status", Title: "Disk temperature sensor status", Units: "status", Priority: 56610,
			Dimensions: []*registry.Dimension{{ID: "ok"}, {ID: "err"}, {ID: "na"}, {ID: "unk"}, {ID: "nos"}, {ID: "slp"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "hddtemp", "hddtemp", "hddtemp"
		ch.Labels = map[string]string{"disk_id": name}
		reg.AddChart(ch)
	}
}

func (h *hddtempCollector) read(ctx context.Context) (string, error) {
	dial := h.dial
	if dial == nil {
		d := net.Dialer{Timeout: h.cfg.Timeout}
		dial = d.DialContext
	}
	conn, err := dial(ctx, "tcp", h.cfg.Address)
	if err != nil {
		return "", fmt.Errorf("hddtemp: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(h.cfg.Timeout))
	b, err := io.ReadAll(io.LimitReader(conn, 1<<20))
	if err != nil {
		return "", fmt.Errorf("hddtemp: %w", err)
	}
	if len(b) == 0 {
		return "", fmt.Errorf("hddtemp: empty")
	}
	return string(b), nil
}

type hddtempDisk struct{ dev, model, temp, unit string }

func parseHddTemp(msg string) ([]hddtempDisk, error) {
	parts := strings.Split(msg, "|")
	var fields []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			fields = append(fields, p)
		}
	}
	if len(fields) == 0 || len(fields)%4 != 0 {
		return nil, fmt.Errorf("hddtemp: invalid output")
	}
	var out []hddtempDisk
	for i := 0; i < len(fields); i += 4 {
		out = append(out, hddtempDisk{dev: fields[i], model: fields[i+1], temp: fields[i+2], unit: fields[i+3]})
	}
	return out, nil
}
