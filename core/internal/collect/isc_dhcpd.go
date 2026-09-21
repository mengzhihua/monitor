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

// iscDHCPPool is one named address range in collectors.modules.isc_dhcpd.pools.
type iscDHCPPool struct {
	Name     string `yaml:"name"`
	Networks string `yaml:"networks"`
}

// iscDHCPDConfig is collectors.modules.isc_dhcpd (dhcpd.leases).
type iscDHCPDConfig struct {
	LeasesPath string        `yaml:"leases_path"`
	Pools      []iscDHCPPool `yaml:"pools"`
}

type iscDHCPDCollector struct {
	cfg      iscDHCPDConfig
	readFile func(path string) ([]byte, error)
	path     string
	seen     map[string]bool
}

func init() {
	Register("isc_dhcpd", func() Collector { return &iscDHCPDCollector{} })
}

func (i *iscDHCPDCollector) Name() string { return "isc_dhcpd" }

func (i *iscDHCPDCollector) Configure(decode func(v any) error) error {
	return decode(&i.cfg)
}

func (i *iscDHCPDCollector) Init(reg *registry.Registry) error {
	if i.readFile == nil {
		i.readFile = os.ReadFile
	}
	i.seen = map[string]bool{}
	paths := []string{i.cfg.LeasesPath}
	if i.cfg.LeasesPath == "" {
		paths = []string{"/var/lib/dhcp/dhcpd.leases", "/var/lib/dhcpd/dhcpd.leases", "/var/db/dhcpd.leases"}
	}
	var last error
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := i.readFile(p); err != nil {
			last = err
			continue
		}
		i.path = p
		break
	}
	if i.path == "" {
		if last == nil {
			last = fmt.Errorf("no leases file")
		}
		return fmt.Errorf("isc_dhcpd: %w", last)
	}
	ch := &registry.Chart{ID: "isc_dhcpd.active_leases_total", Title: "Active Leases Total", Units: "leases", Priority: 59000,
		Dimensions: []*registry.Dimension{{ID: "active"}}}
	ch.Family, ch.Plugin, ch.Module = "isc_dhcpd", "isc_dhcpd", "isc_dhcpd"
	reg.AddChart(ch)
	return nil
}

func (i *iscDHCPDCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	b, err := i.readFile(i.path)
	if err != nil {
		return fmt.Errorf("isc_dhcpd: %w", err)
	}
	active := parseDHCPDLeases(string(b))
	_ = reg.Collect("isc_dhcpd.active_leases_total", now, map[string]float64{"active": float64(len(active))})
	for _, p := range i.cfg.Pools {
		id := sanitizeID(p.Name)
		if !i.seen[id] {
			i.seen[id] = true
			for _, ch := range []*registry.Chart{
				{ID: "isc_dhcpd.dhcp_pool_active_leases." + id, Context: "isc_dhcpd.dhcp_pool_active_leases", Title: "DHCP Pool Active Leases", Units: "leases", Priority: 59010,
					Dimensions: []*registry.Dimension{{ID: "active"}}},
				{ID: "isc_dhcpd.dhcp_pool_utilization." + id, Context: "isc_dhcpd.dhcp_pool_utilization", Title: "DHCP Pool Utilization", Units: "percent", Type: registry.Area, Priority: 59020,
					Dimensions: []*registry.Dimension{{ID: "utilization"}}},
			} {
				ch.Family, ch.Plugin, ch.Module = "isc_dhcpd", "isc_dhcpd", "isc_dhcpd"
				reg.AddChart(ch)
			}
		}
		n, size := countPoolLeases(active, p.Networks)
		util := 0.0
		if size > 0 {
			util = n * 100 / size
		}
		_ = reg.Collect("isc_dhcpd.dhcp_pool_active_leases."+id, now, map[string]float64{"active": n})
		_ = reg.Collect("isc_dhcpd.dhcp_pool_utilization."+id, now, map[string]float64{"utilization": util})
	}
	return nil
}

func parseDHCPDLeases(s string) []net.IP {
	var out []net.IP
	var ip net.IP
	active := false
	flush := func() {
		if active && ip != nil {
			out = append(out, ip)
		}
		ip, active = nil, false
	}
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "lease ") && strings.Contains(t, "{"):
			flush()
			f := strings.Fields(t)
			if len(f) >= 2 {
				ip = net.ParseIP(f[1])
			}
		case strings.HasPrefix(t, "binding state active"):
			active = true
		case t == "}":
			flush()
		}
	}
	flush()
	return out
}

func countPoolLeases(leases []net.IP, networks string) (n, size float64) {
	contains, size := parseIPNets(networks)
	if contains == nil {
		return 0, 0
	}
	for _, ip := range leases {
		if contains(ip) {
			n++
		}
	}
	return n, size
}

func parseIPNets(spec string) (func(net.IP) bool, float64) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, 0
	}
	if strings.Contains(spec, "-") && !strings.Contains(spec, "/") {
		a, b, _ := strings.Cut(spec, "-")
		start, end := parseIPv4(a), parseIPv4(b)
		if start == nil || end == nil {
			return nil, 0
		}
		s, e := ipv4Int(start), ipv4Int(end)
		if e < s {
			s, e = e, s
		}
		return func(ip net.IP) bool {
			v := ip.To4()
			if v == nil {
				return false
			}
			n := ipv4Int(v)
			return n >= s && n <= e
		}, float64(e - s + 1)
	}
	_, n, err := net.ParseCIDR(spec)
	if err != nil {
		ip := parseIPv4(spec)
		if ip == nil {
			return nil, 0
		}
		return func(x net.IP) bool { return x.Equal(ip) }, 1
	}
	ones, bits := n.Mask.Size()
	size := float64(uint64(1) << uint(bits-ones))
	if bits-ones > 24 {
		size = 1 << 24
	}
	return n.Contains, size
}

func ipv4Int(ip net.IP) uint32 {
	ip = ip.To4()
	if ip == nil {
		return 0
	}
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}
