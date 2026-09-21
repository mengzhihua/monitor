package collect

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// openvpnStatusLogConfig is collectors.modules.openvpn_status_log.
type openvpnStatusLogConfig struct {
	LogPath string `yaml:"log_path"`
}

type openvpnStatusLogCollector struct {
	cfg      openvpnStatusLogConfig
	readFile func(path string) ([]byte, error)
	path     string
}

func init() {
	Register("openvpn_status_log", func() Collector { return &openvpnStatusLogCollector{} })
}

func (o *openvpnStatusLogCollector) Name() string { return "openvpn_status_log" }

func (o *openvpnStatusLogCollector) Configure(decode func(v any) error) error {
	return decode(&o.cfg)
}

func (o *openvpnStatusLogCollector) Init(reg *registry.Registry) error {
	if o.readFile == nil {
		o.readFile = os.ReadFile
	}
	paths := []string{o.cfg.LogPath}
	if o.cfg.LogPath == "" {
		paths = []string{"/var/log/openvpn/status.log", "/var/log/openvpn/openvpn-status.log", "/run/openvpn/status.log"}
	}
	var last error
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := o.readFile(p); err != nil {
			last = err
			continue
		}
		o.path = p
		break
	}
	if o.path == "" {
		if last == nil {
			last = fmt.Errorf("no status log")
		}
		return fmt.Errorf("openvpn_status_log: %w", last)
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "openvpn_status_log.active_clients", Context: "openvpn.active_clients", Title: "Active Clients", Units: "active clients", Priority: 59200,
			Dimensions: []*registry.Dimension{{ID: "clients"}}},
		{ID: "openvpn_status_log.total_traffic", Context: "openvpn.total_traffic", Title: "Traffic", Units: "kilobits/s", Type: registry.Area, Priority: 59210,
			Dimensions: []*registry.Dimension{
				{ID: "in", Algorithm: inc, Multiplier: 8, Divisor: 1000},
				{ID: "out", Algorithm: inc, Multiplier: -8, Divisor: 1000},
			}},
	} {
		ch.Family, ch.Plugin, ch.Module = "openvpn", "openvpn_status_log", "openvpn_status_log"
		reg.AddChart(ch)
	}
	return nil
}

func (o *openvpnStatusLogCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	b, err := o.readFile(o.path)
	if err != nil {
		return fmt.Errorf("openvpn_status_log: %w", err)
	}
	n, in, out := parseOpenVPNStatus(string(b))
	_ = reg.Collect("openvpn_status_log.active_clients", now, map[string]float64{"clients": n})
	_ = reg.Collect("openvpn_status_log.total_traffic", now, map[string]float64{"in": in, "out": out})
	return nil
}

func parseOpenVPNStatus(s string) (clients, bytesIn, bytesOut float64) {
	inClients := false
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "OpenVPN CLIENT LIST") || strings.HasPrefix(line, "CLIENT LIST"):
			inClients = true
		case strings.HasPrefix(line, "ROUTING TABLE") || strings.HasPrefix(line, "GLOBAL STATS") || line == "END":
			inClients = false
		case inClients:
			if line == "" || strings.HasPrefix(line, "Updated") || strings.HasPrefix(line, "Common Name") || strings.HasPrefix(line, "HEADER") {
				continue
			}
			f := strings.Split(line, ",")
			if len(f) < 4 {
				continue
			}
			clients++
			bytesIn += parseOpenVPNBytes(f[2])
			bytesOut += parseOpenVPNBytes(f[3])
		}
	}
	return clients, bytesIn, bytesOut
}

func parseOpenVPNBytes(s string) float64 {
	n, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return n
}
