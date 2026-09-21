package collect

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// dnsmasqConfig is collectors.modules.dnsmasq (CHAOS TXT bind stats).
type dnsmasqConfig struct {
	Address string        `yaml:"address"`
	Timeout time.Duration `yaml:"timeout"`
}

type dnsmasqCollector struct {
	cfg   dnsmasqConfig
	query func(ctx context.Context, qname string) ([]string, error)
}

func init() {
	Register("dnsmasq", func() Collector { return &dnsmasqCollector{} })
}

func (d *dnsmasqCollector) Name() string { return "dnsmasq" }

func (d *dnsmasqCollector) Configure(decode func(v any) error) error {
	if err := decode(&d.cfg); err != nil {
		return err
	}
	if d.cfg.Address == "" {
		d.cfg.Address = "127.0.0.1:53"
	}
	if d.cfg.Timeout <= 0 {
		d.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (d *dnsmasqCollector) Init(reg *registry.Registry) error {
	if d.cfg.Address == "" {
		if err := d.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := d.metrics(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "dnsmasq.servers_queries", Title: "Queries forwarded to the upstream servers", Units: "queries/s", Priority: 57100,
			Dimensions: []*registry.Dimension{{ID: "success", Algorithm: inc}, {ID: "failed", Algorithm: inc}}},
		{ID: "dnsmasq.cache_performance", Title: "Cache performance", Units: "events/s", Priority: 57110,
			Dimensions: []*registry.Dimension{{ID: "hits", Algorithm: inc}, {ID: "misses", Algorithm: inc}}},
		{ID: "dnsmasq.cache_operations", Title: "Cache operations", Units: "operations/s", Priority: 57120,
			Dimensions: []*registry.Dimension{{ID: "insertions", Algorithm: inc}, {ID: "evictions", Algorithm: inc}}},
		{ID: "dnsmasq.cache_size", Title: "Cache size", Units: "entries", Priority: 57130,
			Dimensions: []*registry.Dimension{{ID: "size"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "dnsmasq", "dnsmasq", "dnsmasq"
		reg.AddChart(ch)
	}
	return nil
}

func (d *dnsmasqCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := d.metrics(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("dnsmasq.servers_queries", now, map[string]float64{"success": m["queries"], "failed": m["failed_queries"]})
	_ = reg.Collect("dnsmasq.cache_performance", now, map[string]float64{"hits": m["hits"], "misses": m["misses"]})
	_ = reg.Collect("dnsmasq.cache_operations", now, map[string]float64{"insertions": m["insertions"], "evictions": m["evictions"]})
	_ = reg.Collect("dnsmasq.cache_size", now, map[string]float64{"size": m["cachesize"]})
	return nil
}

func (d *dnsmasqCollector) metrics(ctx context.Context) (map[string]float64, error) {
	q := d.query
	if q == nil {
		q = func(ctx context.Context, qname string) ([]string, error) {
			return chaosTXT(ctx, d.cfg.Address, qname, d.cfg.Timeout)
		}
	}
	out := map[string]float64{}
	for _, name := range []string{"cachesize.bind.", "insertions.bind.", "evictions.bind.", "hits.bind.", "misses.bind.", "servers.bind."} {
		txts, err := q(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("dnsmasq: %w", err)
		}
		key := strings.TrimSuffix(strings.Split(name, ".")[0], "")
		if strings.HasPrefix(name, "servers") {
			for _, entry := range txts {
				parts := strings.Fields(entry)
				if len(parts) < 3 {
					continue
				}
				out["queries"] += firstFloat(parts[1])
				out["failed_queries"] += firstFloat(parts[2])
			}
			continue
		}
		if len(txts) > 0 {
			out[key] = firstFloat(txts[0])
		}
	}
	if _, ok := out["cachesize"]; !ok && out["hits"] == 0 && out["misses"] == 0 {
		return nil, fmt.Errorf("dnsmasq: no stats")
	}
	return out, nil
}

func chaosTXT(ctx context.Context, addr, qname string, timeout time.Duration) ([]string, error) {
	payload := encodeDNSQuery(qname)
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "udp", addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(payload); err != nil {
		return nil, err
	}
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	return parseDNSTXT(buf[:n])
}

func encodeDNSQuery(qname string) []byte {
	id := uint16(time.Now().UnixNano())
	buf := make([]byte, 12, 128)
	binary.BigEndian.PutUint16(buf[0:], id)
	buf[2] = 0x01 // RD
	binary.BigEndian.PutUint16(buf[4:], 1)
	for _, lab := range strings.Split(strings.TrimSuffix(qname, "."), ".") {
		if lab == "" {
			continue
		}
		buf = append(buf, byte(len(lab)))
		buf = append(buf, lab...)
	}
	buf = append(buf, 0, 0, 16, 0, 3) // TXT CHAOS
	return buf
}

func parseDNSTXT(msg []byte) ([]string, error) {
	if len(msg) < 12 {
		return nil, fmt.Errorf("short dns")
	}
	if rcode := msg[3] & 0x0f; rcode != 0 {
		return nil, fmt.Errorf("dns rcode %d", rcode)
	}
	qd := binary.BigEndian.Uint16(msg[4:])
	an := binary.BigEndian.Uint16(msg[6:])
	off := 12
	for i := 0; i < int(qd); i++ {
		n, err := skipDNSName(msg, off)
		if err != nil {
			return nil, err
		}
		off = n + 4
	}
	var txts []string
	for i := 0; i < int(an); i++ {
		n, err := skipDNSName(msg, off)
		if err != nil {
			return nil, err
		}
		off = n
		if off+10 > len(msg) {
			break
		}
		typ := binary.BigEndian.Uint16(msg[off:])
		rdlen := int(binary.BigEndian.Uint16(msg[off+8:]))
		off += 10
		if off+rdlen > len(msg) {
			break
		}
		if typ == 16 {
			rdata := msg[off : off+rdlen]
			for len(rdata) > 0 {
				l := int(rdata[0])
				if 1+l > len(rdata) {
					break
				}
				txts = append(txts, string(rdata[1:1+l]))
				rdata = rdata[1+l:]
			}
		}
		off += rdlen
	}
	return txts, nil
}

func skipDNSName(msg []byte, off int) (int, error) {
	for off < len(msg) {
		l := int(msg[off])
		if l == 0 {
			return off + 1, nil
		}
		if l&0xc0 == 0xc0 {
			if off+1 >= len(msg) {
				return 0, fmt.Errorf("bad ptr")
			}
			return off + 2, nil
		}
		off += 1 + l
	}
	return 0, fmt.Errorf("bad name")
}
