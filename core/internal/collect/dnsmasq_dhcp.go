package collect

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// dnsmasqDHCPConfig is collectors.modules.dnsmasq_dhcp (leases file).
type dnsmasqDHCPConfig struct {
	LeasesPath string `yaml:"leases_path"`
	ConfPath   string `yaml:"conf_path"`
}

type dnsmasqDHCPCollector struct {
	cfg        dnsmasqDHCPConfig
	readFile   func(path string) ([]byte, error)
	leasesPath string
}

func init() {
	Register("dnsmasq_dhcp", func() Collector { return &dnsmasqDHCPCollector{} })
}

func (d *dnsmasqDHCPCollector) Name() string { return "dnsmasq_dhcp" }

func (d *dnsmasqDHCPCollector) Configure(decode func(v any) error) error {
	return decode(&d.cfg)
}

func (d *dnsmasqDHCPCollector) Init(reg *registry.Registry) error {
	if d.readFile == nil {
		d.readFile = os.ReadFile
	}
	paths := []string{d.cfg.LeasesPath}
	if d.cfg.LeasesPath == "" {
		paths = []string{"/var/lib/misc/dnsmasq.leases", "/etc/pihole/dhcp.leases", "/var/lib/dnsmasq/dnsmasq.leases"}
	}
	var last error
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := d.readFile(p); err != nil {
			last = err
			continue
		}
		d.leasesPath = p
		break
	}
	if d.leasesPath == "" {
		if last == nil {
			last = fmt.Errorf("no leases file")
		}
		return fmt.Errorf("dnsmasq_dhcp: %w", last)
	}
	for _, ch := range []*registry.Chart{
		{ID: "dnsmasq_dhcp.dhcp_ranges", Title: "Number of DHCP Ranges", Units: "ranges", Type: registry.Stacked, Priority: 58900,
			Dimensions: []*registry.Dimension{{ID: "ipv4"}, {ID: "ipv6"}}},
		{ID: "dnsmasq_dhcp.dhcp_host", Title: "Number of DHCP Hosts", Units: "hosts", Type: registry.Stacked, Priority: 58910,
			Dimensions: []*registry.Dimension{{ID: "ipv4"}, {ID: "ipv6"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "dnsmasq_dhcp", "dnsmasq_dhcp", "dnsmasq_dhcp"
		reg.AddChart(ch)
	}
	return nil
}

func (d *dnsmasqDHCPCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	b, err := d.readFile(d.leasesPath)
	if err != nil {
		return fmt.Errorf("dnsmasq_dhcp: %w", err)
	}
	v4, v6 := parseDnsmasqLeases(string(b))
	r4, r6 := 0.0, 0.0
	conf := d.cfg.ConfPath
	if conf == "" {
		conf = "/etc/dnsmasq.conf"
	}
	if cb, err := d.readFile(conf); err == nil {
		r4, r6 = parseDnsmasqRanges(string(cb))
	}
	_ = reg.Collect("dnsmasq_dhcp.dhcp_ranges", now, map[string]float64{"ipv4": r4, "ipv6": r6})
	_ = reg.Collect("dnsmasq_dhcp.dhcp_host", now, map[string]float64{"ipv4": v4, "ipv6": v6})
	return nil
}

func parseDnsmasqLeases(s string) (ipv4, ipv6 float64) {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "duid") || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		ip := f[2]
		if strings.Contains(ip, ":") {
			ipv6++
		} else {
			ipv4++
		}
	}
	return ipv4, ipv6
}

func parseDnsmasqRanges(s string) (ipv4, ipv6 float64) {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "dhcp-range=") {
			continue
		}
		arg := strings.TrimPrefix(line, "dhcp-range=")
		if strings.Contains(arg, ":") && !strings.Contains(arg, ".") {
			ipv6++
		} else {
			ipv4++
		}
	}
	return ipv4, ipv6
}

func parseIPv4(s string) net.IP {
	ip := net.ParseIP(strings.TrimSpace(s))
	if ip == nil {
		return nil
	}
	return ip.To4()
}
