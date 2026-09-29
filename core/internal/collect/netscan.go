package collect

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

const netscanMaxHosts = 256

type netscanConfig struct {
	Timeout  time.Duration `yaml:"timeout"`
	Networks []netscanNet  `yaml:"networks"`
}

type netscanNet struct {
	Name  string `yaml:"name"`
	CIDR  string `yaml:"cidr"`
	Ports []int  `yaml:"ports"`
}

type netscanCollector struct {
	cfg netscanConfig
}

func init() { Register("netscan", func() Collector { return &netscanCollector{} }) }

func (n *netscanCollector) Name() string { return "netscan" }

func (n *netscanCollector) Configure(decode func(v any) error) error {
	if err := decode(&n.cfg); err != nil {
		return err
	}
	if n.cfg.Timeout <= 0 {
		n.cfg.Timeout = 300 * time.Millisecond
	}
	for _, netw := range n.cfg.Networks {
		if _, err := expandCIDR(netw.CIDR); err != nil {
			return fmt.Errorf("netscan %s: %w", netw.Name, err)
		}
		if len(netw.Ports) == 0 || len(netw.Ports) > 8 {
			return fmt.Errorf("netscan %s: 1..8 ports", netw.Name)
		}
	}
	return nil
}

func (n *netscanCollector) Init(reg *registry.Registry) error {
	if len(n.cfg.Networks) == 0 {
		return fmt.Errorf("netscan: no networks")
	}
	for _, netw := range n.cfg.Networks {
		id := sanitizeID(netw.Name)
		dims := []*registry.Dimension{{ID: "hosts"}, {ID: "open"}}
		reg.AddChart(&registry.Chart{ID: "netscan." + id, Context: "netscan.hosts", Title: "Discovery " + netw.Name,
			Units: "hosts", Family: "discovery", Plugin: "netscan", Module: "netscan", Priority: 56700,
			Labels: map[string]string{"cidr": netw.CIDR}, Dimensions: dims})
	}
	return nil
}

func (n *netscanCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	for _, netw := range n.cfg.Networks {
		hosts, open := ScanCIDR(ctx, netw.CIDR, netw.Ports, n.cfg.Timeout)
		_ = reg.Collect("netscan."+sanitizeID(netw.Name), now, map[string]float64{"hosts": float64(hosts), "open": float64(open)})
	}
	return nil
}

// ScanCIDR connects to each host in cidr on ports. cidr larger than 256 addresses is rejected.
func ScanCIDR(ctx context.Context, cidr string, ports []int, timeout time.Duration) (hosts, open int) {
	list, err := expandCIDR(cidr)
	if err != nil || timeout <= 0 {
		return 0, 0
	}
	hosts = len(list)
	var mu sync.Mutex
	sem := make(chan struct{}, 32)
	var wg sync.WaitGroup
	for _, ip := range list {
		for _, port := range ports {
			if port < 1 || port > 65535 {
				continue
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(ip string, port int) {
				defer wg.Done()
				defer func() { <-sem }()
				d := net.Dialer{Timeout: timeout}
				c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, fmt.Sprint(port)))
				if err == nil {
					_ = c.Close()
					mu.Lock()
					open++
					mu.Unlock()
				}
			}(ip, port)
		}
	}
	wg.Wait()
	return hosts, open
}

func expandCIDR(cidr string) ([]string, error) {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}
	ones, bits := network.Mask.Size()
	if bits != 32 || bits-ones > 8 {
		return nil, fmt.Errorf("cidr %s must be an IPv4 network of at most %d addresses", cidr, netscanMaxHosts)
	}
	start := append(net.IP(nil), network.IP.To4()...)
	var out []string
	for n := start; network.Contains(n); incIP(n) {
		if len(out) > netscanMaxHosts {
			return nil, fmt.Errorf("cidr too large")
		}
		out = append(out, n.String())
	}
	if bits-ones >= 2 && len(out) > 2 {
		out = out[1 : len(out)-1]
	}
	return out, nil
}

func incIP(ip net.IP) {
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			return
		}
	}
}
