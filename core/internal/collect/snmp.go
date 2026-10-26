package collect

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mengzhihua/monitor/core/internal/preprocess"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

// snmpConfig is collectors.modules.snmp (snmpwalk IF-MIB / sysUpTime).
// Version 3 uses USM (user, auth, priv). Items are custom OIDs. LLD walks a
// name column and a value column and opens one dimension chart per row.
type snmpConfig struct {
	Name      string        `yaml:"name"`
	Profile   string        `yaml:"profile"`
	Targets   []snmpConfig  `yaml:"targets"`
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
	Units string `yaml:"units"`
}

type snmpCollector struct {
	cfg       snmpConfig
	run       func(ctx context.Context, name string, args ...string) ([]byte, error)
	seen      map[string]bool
	prev      map[string]float64
	kept      map[string]int64
	targets   []*snmpCollector
	counter64 map[string]bool
}

func init() {
	Register("snmp", func() Collector { return &snmpCollector{} })
}

func (s *snmpCollector) Name() string { return "snmp" }

func (s *snmpCollector) Configure(decode func(v any) error) error {
	if err := decode(&s.cfg); err != nil {
		return err
	}
	if s.cfg.Targets != nil {
		if len(s.cfg.Targets) > 64 || s.cfg.Address != "" {
			return fmt.Errorf("snmp: use up to 64 targets or a legacy address, not both")
		}
		seen := map[string]bool{}
		for i := range s.cfg.Targets {
			t := &s.cfg.Targets[i]
			if !validSNMPDeviceName(t.Name) || seen[t.Name] || strings.TrimSpace(t.Address) == "" || t.Targets != nil {
				return fmt.Errorf("snmp: each target needs a unique name and address; nested targets are unsupported")
			}
			seen[t.Name] = true
			child := &snmpCollector{cfg: *t}
			if child.cfg.Timeout == 0 {
				child.cfg.Timeout = s.cfg.Timeout
			}
			if err := child.Configure(func(any) error { return nil }); err != nil {
				return err
			}
			*t = child.cfg
		}
		return nil
	}
	if s.cfg.Address == "" {
		s.cfg.Address = "127.0.0.1"
	}
	if s.cfg.Community == "" {
		if s.cfg.Name != "" && s.cfg.Version != "3" {
			return fmt.Errorf("snmp: named v1/v2c targets require a community")
		}
		s.cfg.Community = "public"
	}
	if s.cfg.Version == "" {
		s.cfg.Version = "2c"
	}
	if s.cfg.Command == "" {
		s.cfg.Command = "snmpwalk"
	}
	if s.cfg.Timeout <= 0 {
		if s.cfg.Name != "" && s.cfg.Timeout < 0 {
			return fmt.Errorf("snmp: target timeout must be positive")
		}
		s.cfg.Timeout = 3 * time.Second
	}
	if s.cfg.Name != "" && s.cfg.Timeout > time.Minute {
		return fmt.Errorf("snmp: target timeout must not exceed 1m")
	}
	if s.cfg.Profile == "" {
		s.cfg.Profile = "network"
	}
	switch s.cfg.Profile {
	case "network", "ups", "printer", "host":
	default:
		return fmt.Errorf("snmp: unsupported device profile")
	}
	if strings.HasPrefix(s.cfg.Address, "-") || strings.ContainsAny(s.cfg.Address, "\n\r\t ") {
		return fmt.Errorf("snmp: invalid address")
	}
	if s.cfg.Version != "1" && s.cfg.Version != "2c" && s.cfg.Version != "3" {
		return fmt.Errorf("snmp: version must be 1, 2c or 3")
	}
	if s.cfg.PrivPass != "" && s.cfg.AuthPass == "" {
		return fmt.Errorf("snmp: privacy requires authentication")
	}
	if _, err := s.snmpArgs("1.3.6.1.2.1.1.3.0"); err != nil {
		return err
	}
	for _, item := range s.cfg.Items {
		if err := preprocess.Validate(item.Steps); err != nil {
			return fmt.Errorf("snmp: item %q: %w", item.Name, err)
		}
	}
	return nil
}

func (s *snmpCollector) Init(reg *registry.Registry) error {
	if s.cfg.Targets != nil {
		if err := s.Configure(func(any) error { return nil }); err != nil {
			return err
		}
		s.targets = nil
		for _, cfg := range s.cfg.Targets {
			child := &snmpCollector{cfg: cfg, run: s.run}
			child.initCharts(reg)
			s.targets = append(s.targets, child)
		}
		return nil // Offline devices recover on later polls, without blocking startup.
	}
	if s.cfg.Address == "" {
		if err := s.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := s.uptime(context.Background()); err != nil {
		return err
	}
	s.initCharts(reg)
	return nil
}

func (s *snmpCollector) chartID(id string) string {
	if s.cfg.Name == "" {
		return id
	}
	return "snmp.target." + s.cfg.Name + "." + strings.TrimPrefix(id, "snmp.")
}

func (s *snmpCollector) initCharts(reg *registry.Registry) {
	s.seen = map[string]bool{}
	s.prev = map[string]float64{}
	s.kept = map[string]int64{}
	s.counter64 = map[string]bool{}
	c := &registry.Chart{ID: s.chartID("snmp.device_sysuptime"), Context: "snmp.device_sysuptime", Title: "SNMP sysUpTime", Units: "seconds", Priority: 56100,
		Dimensions: []*registry.Dimension{{ID: "uptime"}}}
	c.Family, c.Plugin, c.Module = "snmp", "snmp", "snmp"
	c.Labels = s.deviceLabels()
	reg.AddChart(c)
	status := &registry.Chart{ID: s.chartID("snmp.target_status"), Context: "snmp.target_status", Title: "SNMP device availability", Units: "state", Family: "snmp", Plugin: "snmp", Module: "snmp", Priority: 56090, Labels: s.deviceLabels(), Dimensions: []*registry.Dimension{{ID: "up"}, {ID: "down"}}}
	reg.AddChart(status)
}

func (s *snmpCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	if s.cfg.Targets != nil {
		return s.collectTargets(ctx, reg, now)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	up, err := s.uptime(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		_ = reg.Collect(s.chartID("snmp.target_status"), now, map[string]float64{"up": 0, "down": 1})
		return err
	}
	_ = reg.Collect(s.chartID("snmp.target_status"), now, map[string]float64{"up": 1, "down": 0})
	_ = reg.Collect(s.chartID("snmp.device_sysuptime"), now, map[string]float64{"uptime": up})
	if s.cfg.Profile == "" || s.cfg.Profile == "network" || s.cfg.Profile == "host" {
		ifaces, _ := s.ifaces(ctx)
		inc := registry.Incremental
		for _, iface := range ifaces {
			if err := ctx.Err(); err != nil {
				return err
			}
			id := s.ifaceID(iface)
			s.ensureIface(reg, iface, inc)
			ch, _ := reg.Chart(s.chartID("snmp.device_net." + id))
			if iface.resetIn {
				ch.ResetIncremental("received")
			}
			if iface.resetOut {
				ch.ResetIncremental("sent")
			}
			values := map[string]float64{}
			if !math.IsNaN(iface.in) {
				values["received"] = iface.in
			}
			if !math.IsNaN(iface.out) {
				values["sent"] = iface.out
			}
			_ = reg.Collect(s.chartID("snmp.device_net."+id), now, values)
			if math.IsNaN(iface.oper) {
				continue
			}
			up, down := 0.0, 1.0
			if iface.oper == 1 {
				up, down = 1, 0
			}
			_ = reg.Collect(s.chartID("snmp.device_net_operstatus."+id), now, map[string]float64{"up": up, "down": down})
		}
	}
	s.collectProfile(ctx, reg, now)
	s.collectItems(ctx, reg, now)
	s.collectLLD(ctx, reg, now)
	return ctx.Err()
}

func (s *snmpCollector) collectTargets(ctx context.Context, reg *registry.Registry, now time.Time) error {
	jobs := make(chan *snmpCollector)
	errs := make(chan error, len(s.targets))
	var wg sync.WaitGroup
	for i := 0; i < min(4, len(s.targets)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for child := range jobs {
				if ctx.Err() != nil {
					continue
				}
				cctx, cancel := context.WithTimeout(ctx, child.cfg.Timeout)
				err := child.Collect(cctx, reg, now)
				cancel()
				if err != nil {
					if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
						ch, _ := reg.Chart(child.chartID("snmp.target_status"))
						at, values := ch.LatestValues()
						if at != now.Unix() || values["up"] != 1 { // A slow optional MIB does not make a responding device offline.
							_ = reg.Collect(ch.ID, now, map[string]float64{"up": 0, "down": 1})
						}
					}
					errs <- fmt.Errorf("snmp target %s: collection failed", child.cfg.Name)
				}
			}
		}()
	}
	for _, child := range s.targets {
		select {
		case jobs <- child:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
	close(errs)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var all []error
	for err := range errs {
		all = append(all, err)
	}
	return errors.Join(all...)
}

func validSNMPDeviceName(s string) bool {
	if len(s) == 0 || len(s) > 48 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func (s *snmpCollector) deviceLabels() map[string]string {
	return map[string]string{"device": s.cfg.Address, "target": s.cfg.Name, "profile": s.cfg.Profile}
}

func (s *snmpCollector) collectItems(ctx context.Context, reg *registry.Registry, now time.Time) {
	for _, item := range s.cfg.Items {
		if ctx.Err() != nil {
			return
		}
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
		value, next, keptAt, err := preprocess.ApplyTimed(item.Steps, raw, prev, s.kept[item.Name], now.Unix())
		if errors.Is(err, preprocess.ErrThrottled) {
			continue
		}
		s.prev[item.Name] = next
		if err != nil {
			continue
		}
		s.kept[item.Name] = keptAt
		if ctx.Err() != nil {
			return
		}
		id := s.chartID("snmp.item." + sanitizeID(item.Name))
		if !s.seen[id] {
			s.seen[id] = true
			ch := &registry.Chart{ID: id, Context: "snmp.item", Title: "SNMP " + item.Name, Units: "value", Priority: 56130,
				Labels: s.deviceLabels(), Dimensions: []*registry.Dimension{{ID: "value"}}}
			ch.Labels["oid"] = item.OID
			ch.Family, ch.Plugin, ch.Module = "snmp", "snmp", "snmp"
			reg.AddChart(ch)
		}
		_ = reg.Collect(id, now, map[string]float64{"value": value})
	}
}

func (s *snmpCollector) collectLLD(ctx context.Context, reg *registry.Registry, now time.Time) {
	for _, rule := range s.cfg.LLD {
		if ctx.Err() != nil {
			return
		}
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
		if ctx.Err() != nil {
			return
		}
		names := snmpTableStrings(namesRaw, rule.Table)
		values := snmpTableNumbers(valuesRaw, rule.Value)
		for idx, name := range names {
			name = snmpString(name)
			value, found := values[idx]
			if !found {
				continue
			}
			if name == "" {
				name = idx
			}
			id := s.chartID("snmp.lld." + sanitizeID(rule.Name) + "." + sanitizeID(idx))
			if !s.seen[id] {
				s.seen[id] = true
				units := rule.Units
				if units == "" {
					units = "value"
				}
				ch := &registry.Chart{ID: id, Context: "snmp.lld." + sanitizeID(rule.Name), Title: "SNMP LLD " + rule.Name, Units: units,
					Priority: 56140, Labels: s.deviceLabels(), Dimensions: []*registry.Dimension{{ID: "value"}}}
				ch.Labels["index"], ch.Labels["name"] = idx, name
				ch.Family, ch.Plugin, ch.Module = "snmp", "snmp", "snmp"
				reg.AddChart(ch)
			}
			_ = reg.Collect(id, now, map[string]float64{"value": value})
		}
	}
}

func (s *snmpCollector) ifaceID(iface snmpIface) string {
	if s.cfg.Name != "" {
		return iface.index
	}
	return sanitizeID(iface.name) // Preserve IDs for legacy single-device deployments.
}

func (s *snmpCollector) ensureIface(reg *registry.Registry, iface snmpIface, inc registry.Algorithm) {
	id := s.ifaceID(iface)
	key := s.chartID("snmp.device_net." + id)
	labels := s.deviceLabels()
	labels["ifName"], labels["ifIndex"] = iface.name, iface.index
	if s.seen[key] {
		for _, chartID := range []string{key, s.chartID("snmp.device_net_operstatus." + id)} {
			if ch, ok := reg.Chart(chartID); ok {
				ch.MergeLabels(labels)
			}
		}
		return
	}
	s.seen[key] = true
	io := &registry.Chart{ID: key, Context: "snmp.device_net", Title: "SNMP interface traffic", Units: "B/s",
		Type: registry.Area, Priority: 56110, Labels: labels, Dimensions: []*registry.Dimension{
			{ID: "received", Algorithm: inc}, {ID: "sent", Algorithm: inc, Multiplier: -1}}}
	st := &registry.Chart{ID: s.chartID("snmp.device_net_operstatus." + id), Context: "snmp.device_net_operstatus", Title: "SNMP interface oper status", Units: "state",
		Priority: 56120, Labels: maps.Clone(labels), Dimensions: []*registry.Dimension{{ID: "up"}, {ID: "down"}}}
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
	name              string
	index             string
	in, out           float64
	oper              float64
	resetIn, resetOut bool
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
	reset := map[string]bool{}
	// IF-MIB high-capacity counters avoid 32-bit wraps on fast links. Fallback
	// is per direction/index, since some agents expose only part of ifXTable.
	if s.cfg.Version != "1" {
		hi, _ := s.walk(ctx, "1.3.6.1.2.1.31.1.1.1.6")
		ho, _ := s.walk(ctx, "1.3.6.1.2.1.31.1.1.1.10")
		if s.counter64 == nil {
			s.counter64 = map[string]bool{}
		}
		for _, pair := range []struct {
			key          string
			legacy, high map[string]float64
		}{{"in:", imap, parseSNMPWalk(hi)}, {"out:", omap, parseSNMPWalk(ho)}} {
			for k := range pair.legacy {
				if s.counter64[pair.key+k] {
					delete(pair.legacy, k)
				}
			}
			for k, v := range pair.high {
				reset[pair.key+k] = !s.counter64[pair.key+k]
				pair.legacy[k] = v
				s.counter64[pair.key+k] = true
			}
		}
	}
	var out []snmpIface
	for idx, name := range strMap {
		if name == "" {
			name = idx
		}
		in, inok := imap[idx]
		if !inok {
			in = math.NaN()
		}
		sent, outok := omap[idx]
		if !outok {
			sent = math.NaN()
		}
		oper, operok := smap[idx]
		if !operok {
			oper = math.NaN()
		}
		out = append(out, snmpIface{name: name, index: idx, in: in, out: sent, oper: oper, resetIn: reset["in:"+idx], resetOut: reset["out:"+idx]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].index < out[j].index })
	return out, nil
}

func (s *snmpCollector) walk(ctx context.Context, oid string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
		if v, ok := snmpNumber(rest); ok {
			out[idx] = v
		}
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
		if snmpMissing(rest) {
			continue
		}
		v := strings.TrimSpace(rest)
		if i := strings.Index(v, ":"); i >= 0 {
			v = strings.TrimSpace(v[i+1:])
		}
		v = strings.Trim(v, `"`)
		out[idx] = v
	}
	return out
}

func snmpMissing(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "no such") || strings.Contains(s, "no more variables") || strings.Contains(s, "end of mib")
}

func snmpNumber(s string) (float64, bool) {
	if snmpMissing(s) {
		return 0, false
	}
	if _, value, ok := strings.Cut(s, ":"); ok {
		s = value
	}
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "("); i >= 0 {
		if j := strings.Index(s[i+1:], ")"); j >= 0 {
			s = s[i+1 : i+1+j]
		}
	}
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.Trim(fields[0], `"`), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// Tables such as Printer-MIB use composite indices. Preserve the entire suffix.
func snmpTableStrings(b []byte, base string) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		oid, rest, ok := strings.Cut(sc.Text(), "=")
		oid = strings.TrimPrefix(strings.TrimSpace(oid), ".")
		if !ok || !strings.HasPrefix(oid, base+".") || snmpMissing(rest) {
			continue
		}
		idx := strings.TrimPrefix(oid, base+".")
		if !validOID(idx) {
			continue
		}
		out[idx] = strings.TrimSpace(rest)
	}
	return out
}

func snmpTableNumbers(b []byte, base string) map[string]float64 {
	out := map[string]float64{}
	for idx, rest := range snmpTableStrings(b, base) {
		if v, ok := snmpNumber(rest); ok {
			out[idx] = v
		}
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
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
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
