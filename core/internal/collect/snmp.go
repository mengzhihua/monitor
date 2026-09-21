package collect

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// snmpConfig is collectors.modules.snmp (snmpwalk IF-MIB / sysUpTime).
type snmpConfig struct {
	Address   string        `yaml:"address"`
	Community string        `yaml:"community"`
	Version   string        `yaml:"version"`
	Command   string        `yaml:"command"`
	Timeout   time.Duration `yaml:"timeout"`
}

type snmpCollector struct {
	cfg  snmpConfig
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	seen map[string]bool
}

func init() {
	Register("snmp", func() Collector { return &snmpCollector{} })
}

func (s *snmpCollector) Name() string { return "snmp" }

func (s *snmpCollector) Configure(decode func(v any) error) error {
	if err := decode(&s.cfg); err != nil {
		return err
	}
	if s.cfg.Address == "" {
		s.cfg.Address = "127.0.0.1"
	}
	if s.cfg.Community == "" {
		s.cfg.Community = "public"
	}
	if s.cfg.Version == "" {
		s.cfg.Version = "2c"
	}
	if s.cfg.Command == "" {
		s.cfg.Command = "snmpwalk"
	}
	if s.cfg.Timeout <= 0 {
		s.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (s *snmpCollector) Init(reg *registry.Registry) error {
	if s.cfg.Address == "" {
		if err := s.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	s.seen = map[string]bool{}
	if _, err := s.uptime(context.Background()); err != nil {
		return err
	}
	c := &registry.Chart{ID: "snmp.device_sysuptime", Title: "SNMP sysUpTime", Units: "seconds", Priority: 56100,
		Dimensions: []*registry.Dimension{{ID: "uptime"}}}
	c.Family, c.Plugin, c.Module = "snmp", "snmp", "snmp"
	reg.AddChart(c)
	return nil
}

func (s *snmpCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	up, err := s.uptime(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("snmp.device_sysuptime", now, map[string]float64{"uptime": up})
	ifaces, err := s.ifaces(ctx)
	if err != nil {
		return nil // uptime succeeded; interfaces optional
	}
	inc := registry.Incremental
	for _, iface := range ifaces {
		s.ensureIface(reg, iface.name, inc)
		id := sanitizeID(iface.name)
		_ = reg.Collect("snmp.device_net."+id, now, map[string]float64{"received": iface.in, "sent": iface.out})
		up, down := 0.0, 1.0
		if iface.oper == 1 {
			up, down = 1, 0
		}
		_ = reg.Collect("snmp.device_net_operstatus."+id, now, map[string]float64{"up": up, "down": down})
	}
	return nil
}

func (s *snmpCollector) ensureIface(reg *registry.Registry, name string, inc registry.Algorithm) {
	if s.seen[name] {
		return
	}
	s.seen[name] = true
	id := sanitizeID(name)
	labels := map[string]string{"device": s.cfg.Address, "ifName": name}
	io := &registry.Chart{ID: "snmp.device_net." + id, Context: "snmp.device_net", Title: "SNMP interface traffic", Units: "B/s",
		Type: registry.Area, Priority: 56110, Labels: labels, Dimensions: []*registry.Dimension{
			{ID: "received", Algorithm: inc}, {ID: "sent", Algorithm: inc, Multiplier: -1}}}
	st := &registry.Chart{ID: "snmp.device_net_operstatus." + id, Context: "snmp.device_net_operstatus", Title: "SNMP interface oper status", Units: "state",
		Priority: 56120, Labels: labels, Dimensions: []*registry.Dimension{{ID: "up"}, {ID: "down"}}}
	for _, c := range []*registry.Chart{io, st} {
		c.Family, c.Plugin, c.Module = "snmp", "snmp", "snmp"
		reg.AddChart(c)
	}
}

func (s *snmpCollector) uptime(ctx context.Context) (float64, error) {
	b, err := s.walk(ctx, "1.3.6.1.2.1.1.3.0")
	if err != nil {
		return 0, err
	}
	m := parseSNMPWalk(b)
	for _, v := range m {
		// Timeticks are hundredths of a second
		return v / 100, nil
	}
	return 0, fmt.Errorf("snmp: no sysUpTime")
}

type snmpIface struct {
	name    string
	in, out float64
	oper    float64
}

func (s *snmpCollector) ifaces(ctx context.Context) ([]snmpIface, error) {
	descr, err := s.walk(ctx, "1.3.6.1.2.1.2.2.1.2")
	if err != nil {
		return nil, err
	}
	ins, _ := s.walk(ctx, "1.3.6.1.2.1.2.2.1.10")
	outs, _ := s.walk(ctx, "1.3.6.1.2.1.2.2.1.16")
	opers, _ := s.walk(ctx, "1.3.6.1.2.1.2.2.1.8")
	imap := parseSNMPWalk(ins)
	omap := parseSNMPWalk(outs)
	smap := parseSNMPWalk(opers)
	strMap := parseSNMPWalkStrings(descr)
	var out []snmpIface
	for idx, name := range strMap {
		if name == "" {
			name = idx
		}
		out = append(out, snmpIface{name: name, in: imap[idx], out: omap[idx], oper: smap[idx]})
	}
	return out, nil
}

func (s *snmpCollector) walk(ctx context.Context, oid string) ([]byte, error) {
	run := s.run
	if run == nil {
		run = execRun(s.cfg.Timeout)
	}
	args := []string{"-v", s.cfg.Version, "-c", s.cfg.Community, "-On", "-Oe", s.cfg.Address, oid}
	out, err := run(ctx, s.cfg.Command, args...)
	if err != nil {
		return nil, fmt.Errorf("snmp: %w", err)
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, fmt.Errorf("snmp: empty walk %s", oid)
	}
	return out, nil
}

func parseSNMPWalk(b []byte) map[string]float64 {
	out := map[string]float64{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		oid, rest, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		idx := snmpIndex(oid)
		out[idx] = firstFloat(rest)
	}
	return out
}

func parseSNMPWalkStrings(b []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		oid, rest, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		idx := snmpIndex(oid)
		v := strings.TrimSpace(rest)
		if i := strings.Index(v, ":"); i >= 0 {
			v = strings.TrimSpace(v[i+1:])
		}
		v = strings.Trim(v, `"`)
		out[idx] = v
	}
	return out
}

func snmpIndex(oid string) string {
	oid = strings.TrimSpace(oid)
	if i := strings.LastIndex(oid, "."); i >= 0 {
		return oid[i+1:]
	}
	return oid
}
