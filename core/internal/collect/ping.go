package collect

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

type pingConfig struct {
	Jobs    []pingJob     `yaml:"jobs"`
	Timeout time.Duration `yaml:"timeout"`
}

type pingJob struct {
	Name     string        `yaml:"name"`
	Host     string        `yaml:"host"`
	Protocol string        `yaml:"protocol"` // icmp (default) or tcp
	Port     int           `yaml:"port"`     // for tcp, default 80
	Timeout  time.Duration `yaml:"timeout"`
}

type pingCollector struct {
	cfg pingConfig
	id  uint16
}

func init() {
	Register("ping", func() Collector { return &pingCollector{id: uint16(os.Getpid())} })
}

func (p *pingCollector) Name() string { return "ping" }

func (p *pingCollector) Configure(decode func(v any) error) error {
	if err := decode(&p.cfg); err != nil {
		return err
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (p *pingCollector) Init(reg *registry.Registry) error {
	if p.cfg.Timeout <= 0 {
		if err := p.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if len(p.cfg.Jobs) == 0 {
		return fmt.Errorf("no jobs configured")
	}
	for i := range p.cfg.Jobs {
		j := &p.cfg.Jobs[i]
		if j.Host == "" {
			return fmt.Errorf("ping job %q: host required", j.Name)
		}
		if j.Name == "" {
			j.Name = j.Host
		}
		if j.Protocol == "" {
			j.Protocol = "icmp"
		}
		if j.Port <= 0 {
			j.Port = 80
		}
		if j.Timeout <= 0 {
			j.Timeout = p.cfg.Timeout
		}
		id := sanitizeID(j.Name)
		lbl := map[string]string{"job": j.Name, "host": j.Host}
		reg.AddChart(&registry.Chart{ID: "ping.latency." + id, Context: "ping.latency", Family: j.Name,
			Title: "Ping latency", Units: "ms", Priority: 52000, Plugin: "ping", Module: "ping",
			Labels: lbl, Dimensions: []*registry.Dimension{{ID: "rtt"}}})
		reg.AddChart(&registry.Chart{ID: "ping.loss." + id, Context: "ping.loss", Family: j.Name,
			Title: "Ping packet loss", Units: "packets", Priority: 52001, Plugin: "ping", Module: "ping",
			Labels: lbl, Dimensions: []*registry.Dimension{{ID: "received"}, {ID: "dropped"}}})
	}
	return nil
}

func (p *pingCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	for i, j := range p.cfg.Jobs {
		id := sanitizeID(j.Name)
		rtt, err := p.probe(ctx, j, uint16(i+1))
		recv, drop := 1.0, 0.0
		if err != nil {
			recv, drop, rtt = 0, 1, 0
		}
		_ = reg.Collect("ping.latency."+id, now, map[string]float64{"rtt": rtt})
		_ = reg.Collect("ping.loss."+id, now, map[string]float64{"received": recv, "dropped": drop})
	}
	return nil
}

func (p *pingCollector) probe(ctx context.Context, j pingJob, seq uint16) (float64, error) {
	if j.Protocol == "tcp" {
		d := net.Dialer{Timeout: j.Timeout}
		cctx, cancel := context.WithTimeout(ctx, j.Timeout)
		defer cancel()
		start := time.Now()
		conn, err := d.DialContext(cctx, "tcp", net.JoinHostPort(j.Host, fmt.Sprint(j.Port)))
		if err != nil {
			return 0, err
		}
		_ = conn.Close()
		return float64(time.Since(start).Microseconds()) / 1000, nil
	}
	return icmpPing(j.Host, j.Timeout, p.id, seq)
}

func icmpPing(host string, timeout time.Duration, id, seq uint16) (float64, error) {
	ip, err := net.ResolveIPAddr("ip4", host)
	if err != nil {
		return 0, err
	}
	conn, err := net.DialIP("ip4:icmp", nil, ip)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	payload := []byte("monitor-ping")
	pkt := icmpEcho(id, seq, payload)
	start := time.Now()
	if _, err := conn.Write(pkt); err != nil {
		return 0, err
	}
	buf := make([]byte, 1500)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return 0, err
		}
		// IP header is stripped by DialIP on some OSes and present on others.
		b := buf[:n]
		if len(b) > 20 && b[0]>>4 == 4 {
			ihl := int(b[0]&0x0f) * 4
			if n > ihl {
				b = b[ihl:]
			}
		}
		if len(b) < 8 || b[0] != 0 { // echo reply
			continue
		}
		gotID := binary.BigEndian.Uint16(b[4:6])
		gotSeq := binary.BigEndian.Uint16(b[6:8])
		if gotID != id || gotSeq != seq {
			continue
		}
		return float64(time.Since(start).Microseconds()) / 1000, nil
	}
}

func icmpEcho(id, seq uint16, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	b[0] = 8 // echo request
	binary.BigEndian.PutUint16(b[4:6], id)
	binary.BigEndian.PutUint16(b[6:8], seq)
	copy(b[8:], payload)
	cs := icmpChecksum(b)
	binary.BigEndian.PutUint16(b[2:4], cs)
	return b
}

func icmpChecksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	return ^uint16(sum)
}
