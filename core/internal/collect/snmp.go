package collect

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/preprocess"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

// snmpConfig is collectors.modules.snmp (snmpwalk IF-MIB / sysUpTime).
// Version 3 uses USM (user, auth, priv). Items are custom OIDs. LLD walks a
// name column and a value column and opens one dimension chart per row.
type snmpConfig struct {
	Address   string        `yaml:"address"`
	Community string        `yaml:"community"`
	Version   string        `yaml:"version"`
	Command   string        `yaml:"command"`
	Timeout   time.Duration `yaml:"timeout"`
	User      string        `yaml:"user"`
	AuthProto string        `yaml:"auth_protocol"`
	AuthPass  string        `yaml:"auth_passphrase"`
	PrivProto string        `yaml:"priv_protocol"`
	PrivPass  string        `yaml:"priv_passphrase"`
	Items     []snmpItem    `yaml:"items"`
	LLD       []snmpLLD     `yaml:"lld"`
}

type snmpItem struct {
	Name  string            `yaml:"name"`
	OID   string            `yaml:"oid"`
	Steps []preprocess.Step `yaml:"preprocess"`
}

type snmpLLD struct {
	Name  string `yaml:"name"`
	Table string `yaml:"table"`
	Value string `yaml:"value"`
}

type snmpCollector struct {
	cfg  snmpConfig
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	seen map[string]bool
	prev map[string]float64
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
	s.prev = map[string]float64{}
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
	s.collectItems(ctx, reg, now)
	s.collectLLD(ctx, reg, now)
	return nil
}

func (s *snmpCollector) collectItems(ctx context.Context, reg *registry.Registry, now time.Time) {
	for _, item := range s.cfg.Items {
		if !validOID(item.OID) || !validItemName(item.Name) {
			continue
		}
		b, err := s.walk(ctx, item.OID)
		if err != nil {
			continue
		}
		raw := firstSNMPString(b)
		prev := s.prev[item.Name]
		if _, ok := s.prev[item.Name]; !ok {
			prev = math.NaN()
		}
		value, next, err := preprocess.Apply(item.Steps, raw, prev)
		s.prev[item.Name] = next
		if err != nil {
			continue
		}
		id := "snmp.item." + sanitizeID(item.Name)
		if !s.seen[id] {
			s.seen[id] = true
			ch := &registry.Chart{ID: id, Context: "snmp.item", Title: "SNMP " + item.Name, Units: "value", Priority: 56130,
				Labels: map[string]string{"oid": item.OID}, Dimensions: []*registry.Dimension{{ID: "value"}}}
			ch.Family, ch.Plugin, ch.Module = "snmp", "snmp", "snmp"
			reg.AddChart(ch)
		}
		_ = reg.Collect(id, now, map[string]float64{"value": value})
	}
}

func (s *snmpCollector) collectLLD(ctx context.Context, reg *registry.Registry, now time.Time) {
	for _, rule := range s.cfg.LLD {
		if !validOID(rule.Table) || !validOID(rule.Value) || !validItemName(rule.Name) {
			continue
		}
		namesRaw, err := s.walk(ctx, rule.Table)
		if err != nil {
			continue
		}
		valuesRaw, err := s.walk(ctx, rule.Value)
		if err != nil {
			continue
		}
		names := parseSNMPWalkStrings(namesRaw)
		values := parseSNMPWalk(valuesRaw)
		for idx, name := range names {
			if name == "" {
				name = idx
			}
			id := "snmp.lld." + sanitizeID(rule.Name) + "." + sanitizeID(idx)
			if !s.seen[id] {
				s.seen[id] = true
				ch := &registry.Chart{ID: id, Context: "snmp.lld." + sanitizeID(rule.Name), Title: "SNMP LLD " + rule.Name, Units: "value",
					Priority: 56140, Labels: map[string]string{"index": idx, "name": name}, Dimensions: []*registry.Dimension{{ID: "value"}}}
				ch.Family, ch.Plugin, ch.Module = "snmp", "snmp", "snmp"
				reg.AddChart(ch)
			}
			_ = reg.Collect(id, now, map[string]float64{"value": values[idx]})
		}
	}
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
	args, err := s.snmpArgs(oid)
	if err != nil {
		return nil, err
	}
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

func (s *snmpCollector) snmpArgs(oid string) ([]string, error) {
	if !validOID(oid) {
		return nil, fmt.Errorf("snmp: oid %q", oid)
	}
	if strings.EqualFold(s.cfg.Version, "3") {
		if s.cfg.User == "" || strings.ContainsAny(s.cfg.User, " \t\n") {
			return nil, fmt.Errorf("snmp: v3 user required")
		}
		level := "noAuthNoPriv"
		if s.cfg.AuthPass != "" && s.cfg.PrivPass != "" {
			level = "authPriv"
		} else if s.cfg.AuthPass != "" {
			level = "authNoPriv"
		}
		args := []string{"-v", "3", "-l", level, "-u", s.cfg.User}
		if s.cfg.AuthPass != "" {
			proto := s.cfg.AuthProto
			if proto == "" {
				proto = "SHA"
			}
			if !validSNMPToken(proto) || strings.ContainsAny(s.cfg.AuthPass, "\n\r") {
				return nil, fmt.Errorf("snmp: bad auth settings")
			}
			args = append(args, "-a", proto, "-A", s.cfg.AuthPass)
		}
		if s.cfg.PrivPass != "" {
			proto := s.cfg.PrivProto
			if proto == "" {
				proto = "AES"
			}
			if !validSNMPToken(proto) || strings.ContainsAny(s.cfg.PrivPass, "\n\r") {
				return nil, fmt.Errorf("snmp: bad priv settings")
			}
			args = append(args, "-x", proto, "-X", s.cfg.PrivPass)
		}
		return append(args, "-On", "-Oe", s.cfg.Address, oid), nil
	}
	return []string{"-v", s.cfg.Version, "-c", s.cfg.Community, "-On", "-Oe", s.cfg.Address, oid}, nil
}

func validOID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	dot := false
	for _, r := range s {
		if r == '.' {
			if dot {
				return false
			}
			dot = true
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
		dot = false
	}
	return !dot && !strings.HasPrefix(s, ".")
}

func validSNMPToken(s string) bool {
	if s == "" || len(s) > 16 {
		return false
	}
	for _, r := range s {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func validItemName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}

func firstSNMPString(b []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(b))
	if !sc.Scan() {
		return ""
	}
	_, rest, ok := strings.Cut(sc.Text(), "=")
	if !ok {
		return strings.TrimSpace(sc.Text())
	}
	v := strings.TrimSpace(rest)
	if i := strings.Index(v, ":"); i >= 0 {
		v = strings.TrimSpace(v[i+1:])
	}
	return strings.Trim(v, `"`)
}

func snmpIndex(oid string) string {
	oid = strings.TrimSpace(oid)
	if i := strings.LastIndex(oid, "."); i >= 0 {
		return oid[i+1:]
	}
	return oid
}
