package collect

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// snmpTopologyConfig is collectors.modules.snmp_topology (snmpwalk LLDP rem table).
type snmpTopologyConfig struct {
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
}

type snmpTopologyCollector struct {
	cfg  snmpTopologyConfig
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	seen map[string]bool
}

func init() {
	Register("snmp_topology", func() Collector { return &snmpTopologyCollector{} })
}

func (s *snmpTopologyCollector) Name() string { return "snmp_topology" }

func (s *snmpTopologyCollector) Configure(decode func(v any) error) error {
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
		s.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (s *snmpTopologyCollector) Init(reg *registry.Registry) error {
	if s.cfg.Command == "" {
		if err := s.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	s.seen = map[string]bool{}
	n, err := s.neighbors(context.Background())
	if err != nil {
		return err
	}
	if len(n) == 0 {
		return fmt.Errorf("snmp_topology: no neighbors")
	}
	for _, ch := range []*registry.Chart{
		{ID: "snmp_topology.devices", Context: "netdata.go.plugin.collector.snmp_topology.devices", Title: "SNMP topology devices",
			Units: "devices", Family: "Internal", Priority: 62900, Dimensions: []*registry.Dimension{{ID: "registered"}, {ID: "cached"}}},
		{ID: "snmp_topology.refreshes", Context: "netdata.go.plugin.collector.snmp_topology.refreshes", Title: "SNMP topology refreshes",
			Units: "events/s", Family: "Internal", Priority: 62910, Dimensions: []*registry.Dimension{
				{ID: "runs", Algorithm: registry.Incremental}, {ID: "errors", Algorithm: registry.Incremental}}},
	} {
		ch.Plugin, ch.Module = "snmp_topology", "snmp_topology"
		reg.AddChart(ch)
	}
	return nil
}

func (s *snmpTopologyCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	n, err := s.neighbors(ctx)
	if err != nil {
		_ = reg.Collect("snmp_topology.refreshes", now, map[string]float64{"runs": 1, "errors": 1})
		return err
	}
	_ = reg.Collect("snmp_topology.devices", now, map[string]float64{"registered": float64(len(n)), "cached": float64(len(s.seen))})
	_ = reg.Collect("snmp_topology.refreshes", now, map[string]float64{"runs": 1, "errors": 0})
	for _, name := range n {
		s.ensureNeighbor(reg, name)
		_ = reg.Collect("snmp_topology.lldp_neighbor."+sanitizeID(name), now, map[string]float64{"present": 1})
	}
	return nil
}

func (s *snmpTopologyCollector) ensureNeighbor(reg *registry.Registry, name string) {
	if s.seen[name] {
		return
	}
	s.seen[name] = true
	ch := &registry.Chart{
		ID: "snmp_topology.lldp_neighbor." + sanitizeID(name), Context: "snmp_topology.lldp_neighbor",
		Title: "LLDP neighbor", Units: "state", Family: "lldp", Priority: 62920,
		Labels: map[string]string{"neighbor": name}, Dimensions: []*registry.Dimension{{ID: "present"}},
	}
	ch.Plugin, ch.Module = "snmp_topology", "snmp_topology"
	reg.AddChart(ch)
}

func (s *snmpTopologyCollector) neighbors(ctx context.Context) ([]string, error) {
	run := s.run
	if run == nil {
		run = execRun(s.cfg.Timeout)
	}
	// lldpRemSysName. v3 uses the same USM flags as the snmp collector.
	args, err := (&snmpCollector{cfg: snmpConfig{
		Address: s.cfg.Address, Community: s.cfg.Community, Version: s.cfg.Version,
		User: s.cfg.User, AuthProto: s.cfg.AuthProto, AuthPass: s.cfg.AuthPass,
		PrivProto: s.cfg.PrivProto, PrivPass: s.cfg.PrivPass,
	}}).snmpArgs("1.0.8802.1.1.2.1.4.1.1.9")
	if err != nil {
		return nil, err
	}
	b, err := run(ctx, s.cfg.Command, args...)
	if err != nil {
		return nil, fmt.Errorf("snmp_topology: %w", err)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, fmt.Errorf("snmp_topology: empty walk")
	}
	names := parseSNMPWalkStrings(b)
	var out []string
	for _, v := range names {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("snmp_topology: no neighbors")
	}
	return out, nil
}
