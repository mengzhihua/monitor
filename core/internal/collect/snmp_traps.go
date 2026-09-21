package collect

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// snmpTrapsConfig is collectors.modules.snmp_traps (UDP SNMPv1/v2c listener).
type snmpTrapsConfig struct {
	Listen string `yaml:"listen"` // default 127.0.0.1:9162 (non-root)
}

type snmpTrapsCollector struct {
	cfg  snmpTrapsConfig
	conn *net.UDPConn

	mu        sync.Mutex
	received  float64
	decoded   float64
	accepted  float64
	dropped   float64
	unknown   float64
	decodeErr float64
	malformed float64
}

func init() {
	Register("snmp_traps", func() Collector { return &snmpTrapsCollector{} })
}

func (s *snmpTrapsCollector) Name() string { return "snmp_traps" }

func (s *snmpTrapsCollector) Configure(decode func(v any) error) error {
	if err := decode(&s.cfg); err != nil {
		return err
	}
	if s.cfg.Listen == "" {
		s.cfg.Listen = "127.0.0.1:9162"
	}
	return nil
}

func (s *snmpTrapsCollector) Init(reg *registry.Registry) error {
	if s.cfg.Listen == "" {
		if err := s.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	addr, err := net.ResolveUDPAddr("udp", s.cfg.Listen)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("snmp_traps: %w", err)
	}
	s.conn = conn
	go s.readLoop()
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "snmp.trap.pipeline", Context: "snmp.trap.pipeline", Title: "SNMP trap receiver pipeline", Units: "events/s", Family: "traps", Priority: 62800,
			Dimensions: []*registry.Dimension{
				{ID: "received", Algorithm: inc}, {ID: "decoded", Algorithm: inc}, {ID: "accepted", Algorithm: inc},
				{ID: "dropped", Algorithm: inc}}},
		{ID: "snmp.trap.events", Context: "snmp.trap.events", Title: "SNMP trap events", Units: "events/s", Family: "traps", Type: registry.Stacked, Priority: 62810,
			Dimensions: []*registry.Dimension{{ID: "unknown", Algorithm: inc}}},
		{ID: "snmp.trap.errors", Context: "snmp.trap.errors", Title: "SNMP trap processing errors", Units: "errors/s", Family: "traps", Type: registry.Stacked, Priority: 62820,
			Dimensions: []*registry.Dimension{
				{ID: "decode_failed", Algorithm: inc}, {ID: "malformed_pdu", Algorithm: inc}}},
	} {
		ch.Plugin, ch.Module = "snmp_traps", "snmp_traps"
		reg.AddChart(ch)
	}
	return nil
}

func (s *snmpTrapsCollector) Stop() {
	if s.conn != nil {
		_ = s.conn.Close()
	}
}

func (s *snmpTrapsCollector) readLoop() {
	buf := make([]byte, 65535)
	for {
		n, _, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		s.ingest(buf[:n])
	}
}

func (s *snmpTrapsCollector) ingest(b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.received++
	if len(b) == 0 || b[0] != 0x30 {
		s.decodeErr++
		s.dropped++
		return
	}
	if _, _, _, ok := parseSNMPTrap(b); !ok {
		s.malformed++
		s.dropped++
		return
	}
	s.decoded++
	s.accepted++
	s.unknown++
}

func (s *snmpTrapsCollector) Collect(_ context.Context, reg *registry.Registry, now time.Time) error {
	s.mu.Lock()
	recv, dec, acc, drop := s.received, s.decoded, s.accepted, s.dropped
	unk, df, mal := s.unknown, s.decodeErr, s.malformed
	s.mu.Unlock()
	_ = reg.Collect("snmp.trap.pipeline", now, map[string]float64{"received": recv, "decoded": dec, "accepted": acc, "dropped": drop})
	_ = reg.Collect("snmp.trap.events", now, map[string]float64{"unknown": unk})
	_ = reg.Collect("snmp.trap.errors", now, map[string]float64{"decode_failed": df, "malformed_pdu": mal})
	return nil
}

func parseSNMPTrap(b []byte) (version int, community string, trapOID string, ok bool) {
	_, rest, ok := berTLV(b)
	if !ok {
		return 0, "", "", false
	}
	ver, rest, ok := berInt(rest)
	if !ok {
		return 0, "", "", false
	}
	comm, rest, ok := berOctet(rest)
	if !ok {
		return 0, "", "", false
	}
	if len(rest) == 0 {
		return ver, string(comm), "", false
	}
	tag := rest[0]
	// v1 trap 0xA4, v2 trap 0xA7, inform 0xA8
	if tag != 0xA4 && tag != 0xA7 && tag != 0xA8 {
		return ver, string(comm), "", false
	}
	return ver, string(comm), fmt.Sprintf("%02x", tag), true
}

func berTLV(b []byte) (tag byte, rest []byte, ok bool) {
	if len(b) < 2 {
		return 0, nil, false
	}
	tag = b[0]
	n, hdr, ok := berLen(b[1:])
	if !ok || len(b) < 1+hdr+n {
		return 0, nil, false
	}
	return tag, b[1+hdr : 1+hdr+n], true
}

func berLen(b []byte) (n, hdr int, ok bool) {
	if len(b) == 0 {
		return 0, 0, false
	}
	if b[0]&0x80 == 0 {
		return int(b[0]), 1, true
	}
	cnt := int(b[0] & 0x7f)
	if cnt == 0 || cnt > 4 || len(b) < 1+cnt {
		return 0, 0, false
	}
	var v int
	for i := 0; i < cnt; i++ {
		v = (v << 8) | int(b[1+i])
	}
	return v, 1 + cnt, true
}

func berInt(b []byte) (int, []byte, bool) {
	if len(b) == 0 || b[0] != 0x02 {
		return 0, b, false
	}
	_, inner, ok := berTLV(b)
	if !ok {
		return 0, b, false
	}
	n, hdr, ok := berLen(b[1:])
	if !ok {
		return 0, b, false
	}
	v := 0
	for _, c := range inner {
		v = (v << 8) | int(c)
	}
	return v, b[1+hdr+n:], true
}

func berOctet(b []byte) ([]byte, []byte, bool) {
	if len(b) == 0 || b[0] != 0x04 {
		return nil, b, false
	}
	_, inner, ok := berTLV(b)
	if !ok {
		return nil, b, false
	}
	n, hdr, ok := berLen(b[1:])
	if !ok {
		return nil, b, false
	}
	return inner, b[1+hdr+n:], true
}
