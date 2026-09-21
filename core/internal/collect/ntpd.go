package collect

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// ntpdConfig is collectors.modules.ntpd (Netdata go.d ntpd). Uses NTP client
// mode-3 packets against the daemon (default 127.0.0.1:123).
type ntpdConfig struct {
	Address string        `yaml:"address"`
	Timeout time.Duration `yaml:"timeout"`
}

type ntpdCollector struct {
	cfg ntpdConfig
}

func init() {
	Register("ntpd", func() Collector { return &ntpdCollector{} })
}

func (n *ntpdCollector) Name() string { return "ntpd" }

func (n *ntpdCollector) Configure(decode func(v any) error) error {
	if err := decode(&n.cfg); err != nil {
		return err
	}
	if n.cfg.Address == "" {
		n.cfg.Address = "127.0.0.1:123"
	}
	if n.cfg.Timeout <= 0 {
		n.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (n *ntpdCollector) Init(reg *registry.Registry) error {
	if n.cfg.Address == "" {
		if err := n.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := n.query(context.Background()); err != nil {
		return err
	}
	for _, c := range []*registry.Chart{
		{ID: "ntpd.sys_stratum", Title: "NTPd stratum", Units: "stratum", Priority: 48100, Dimensions: []*registry.Dimension{{ID: "stratum"}}},
		{ID: "ntpd.sys_offset", Title: "NTPd offset", Units: "milliseconds", Priority: 48110, Dimensions: []*registry.Dimension{{ID: "offset"}}},
		{ID: "ntpd.sys_delay", Title: "NTPd round-trip delay", Units: "milliseconds", Priority: 48120, Dimensions: []*registry.Dimension{{ID: "delay"}}},
		{ID: "ntpd.sys_rootdelay", Title: "NTPd root delay", Units: "milliseconds", Priority: 48130, Dimensions: []*registry.Dimension{{ID: "rootdelay"}}},
		{ID: "ntpd.sys_rootdisp", Title: "NTPd root dispersion", Units: "milliseconds", Priority: 48140, Dimensions: []*registry.Dimension{{ID: "rootdisp"}}},
		{ID: "ntpd.sys_precision", Title: "NTPd precision", Units: "log2", Priority: 48150, Dimensions: []*registry.Dimension{{ID: "precision"}}},
	} {
		c.Family, c.Plugin, c.Module = "ntpd", "ntpd", "ntpd"
		reg.AddChart(c)
	}
	return nil
}

type ntpSample struct {
	stratum, offsetMs, delayMs, rootDelayMs, rootDispMs, precision float64
}

func (n *ntpdCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	s, err := n.query(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("ntpd.sys_stratum", now, map[string]float64{"stratum": s.stratum})
	_ = reg.Collect("ntpd.sys_offset", now, map[string]float64{"offset": s.offsetMs})
	_ = reg.Collect("ntpd.sys_delay", now, map[string]float64{"delay": s.delayMs})
	_ = reg.Collect("ntpd.sys_rootdelay", now, map[string]float64{"rootdelay": s.rootDelayMs})
	_ = reg.Collect("ntpd.sys_rootdisp", now, map[string]float64{"rootdisp": s.rootDispMs})
	_ = reg.Collect("ntpd.sys_precision", now, map[string]float64{"precision": s.precision})
	return nil
}

func (n *ntpdCollector) query(ctx context.Context) (ntpSample, error) {
	d := net.Dialer{Timeout: n.cfg.Timeout}
	conn, err := d.DialContext(ctx, "udp", n.cfg.Address)
	if err != nil {
		return ntpSample{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(n.cfg.Timeout))
	t1 := time.Now()
	pkt := make([]byte, 48)
	pkt[0] = 0x23 // LI=0 VN=4 Mode=3
	binary.BigEndian.PutUint64(pkt[40:], ntpTimestamp(t1))
	if _, err := conn.Write(pkt); err != nil {
		return ntpSample{}, err
	}
	buf := make([]byte, 48)
	nr, err := conn.Read(buf)
	t4 := time.Now()
	if err != nil {
		return ntpSample{}, err
	}
	return parseNTPReply(buf[:nr], t1, t4)
}

func parseNTPReply(buf []byte, t1, t4 time.Time) (ntpSample, error) {
	if len(buf) < 48 {
		return ntpSample{}, fmt.Errorf("ntpd: short reply")
	}
	stratum := float64(buf[1])
	if stratum == 0 {
		return ntpSample{}, fmt.Errorf("ntpd: kiss-o'-death / unsynchronised")
	}
	precision := float64(int8(buf[3]))
	rootDelay := ntpShortMs(buf[4:8])
	rootDisp := ntpShortMs(buf[8:12])
	t2 := ntpTime(buf[32:40])
	t3 := ntpTime(buf[40:48])
	// offset = ((T2-T1)+(T3-T4))/2 ; delay = (T4-T1)-(T3-T2)
	offset := t2.Sub(t1) + t3.Sub(t4)
	offset /= 2
	delay := t4.Sub(t1) - t3.Sub(t2)
	return ntpSample{
		stratum: stratum, precision: precision,
		offsetMs:    float64(offset.Microseconds()) / 1000,
		delayMs:     float64(delay.Microseconds()) / 1000,
		rootDelayMs: rootDelay, rootDispMs: rootDisp,
	}, nil
}

func ntpTimestamp(t time.Time) uint64 {
	const ntpEpoch = 2208988800
	s := uint64(t.Unix()) + ntpEpoch
	f := uint64(t.Nanosecond()) * (1 << 32) / 1e9
	return s<<32 | f
}

func ntpTime(b []byte) time.Time {
	if len(b) < 8 {
		return time.Time{}
	}
	v := binary.BigEndian.Uint64(b)
	const ntpEpoch = 2208988800
	sec := int64(v>>32) - ntpEpoch
	frac := v & 0xffffffff
	nsec := int64(frac) * 1e9 / (1 << 32)
	return time.Unix(sec, nsec)
}

func ntpShortMs(b []byte) float64 {
	if len(b) < 4 {
		return 0
	}
	v := binary.BigEndian.Uint32(b)
	sec := float64(v) / (1 << 16)
	return sec * 1000
}
