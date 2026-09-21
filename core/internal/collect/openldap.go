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

// openldapConfig is collectors.modules.openldap (cn=Monitor via ldapsearch).
type openldapConfig struct {
	URL      string        `yaml:"url"`
	BindDN   string        `yaml:"bind_dn"`
	Password string        `yaml:"password"`
	Command  string        `yaml:"command"`
	Timeout  time.Duration `yaml:"timeout"`
}

type openldapCollector struct {
	cfg openldapConfig
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func init() {
	Register("openldap", func() Collector { return &openldapCollector{} })
}

func (o *openldapCollector) Name() string { return "openldap" }

func (o *openldapCollector) Configure(decode func(v any) error) error {
	if err := decode(&o.cfg); err != nil {
		return err
	}
	if o.cfg.URL == "" {
		o.cfg.URL = "ldap://127.0.0.1:389"
	}
	if o.cfg.Command == "" {
		o.cfg.Command = "ldapsearch"
	}
	if o.cfg.Timeout <= 0 {
		o.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (o *openldapCollector) Init(reg *registry.Registry) error {
	if o.cfg.Command == "" {
		if err := o.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := o.monitor(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "openldap.current_connections", Title: "Current Connections", Units: "connections", Priority: 52600,
			Dimensions: []*registry.Dimension{{ID: "active"}}},
		{ID: "openldap.connections", Title: "Connections", Units: "connections/s", Priority: 52610,
			Dimensions: []*registry.Dimension{{ID: "connections", Algorithm: inc}}},
		{ID: "openldap.traffic", Title: "Traffic", Units: "bytes/s", Type: registry.Area, Priority: 52620,
			Dimensions: []*registry.Dimension{{ID: "sent", Algorithm: inc}}},
		{ID: "openldap.entries", Title: "Entries", Units: "entries/s", Priority: 52630,
			Dimensions: []*registry.Dimension{{ID: "sent", Algorithm: inc}}},
		{ID: "openldap.referrals", Title: "Referrals", Units: "referrals/s", Priority: 52640,
			Dimensions: []*registry.Dimension{{ID: "sent", Algorithm: inc}}},
		{ID: "openldap.operations", Title: "Operations", Units: "operations/s", Priority: 52650,
			Dimensions: []*registry.Dimension{{ID: "completed", Algorithm: inc}, {ID: "initiated", Algorithm: inc}}},
		{ID: "openldap.operations_by_type", Title: "Operations by Type", Units: "operations/s", Type: registry.Stacked, Priority: 52660,
			Dimensions: []*registry.Dimension{
				{ID: "bind", Algorithm: inc}, {ID: "search", Algorithm: inc}, {ID: "unbind", Algorithm: inc},
				{ID: "add", Algorithm: inc}, {ID: "delete", Algorithm: inc}, {ID: "modify", Algorithm: inc},
				{ID: "compare", Algorithm: inc}}},
		{ID: "openldap.waiters", Title: "Waiters", Units: "waiters", Priority: 52670,
			Dimensions: []*registry.Dimension{{ID: "read"}, {ID: "write"}}},
	} {
		c.Family, c.Plugin, c.Module = "openldap", "openldap", "openldap"
		reg.AddChart(c)
	}
	return nil
}

func (o *openldapCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := o.monitor(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("openldap.current_connections", now, map[string]float64{"active": m["current_connections"]})
	_ = reg.Collect("openldap.connections", now, map[string]float64{"connections": m["total_connections"]})
	_ = reg.Collect("openldap.traffic", now, map[string]float64{"sent": m["bytes_sent"]})
	_ = reg.Collect("openldap.entries", now, map[string]float64{"sent": m["entries_sent"]})
	_ = reg.Collect("openldap.referrals", now, map[string]float64{"sent": m["referrals_sent"]})
	_ = reg.Collect("openldap.operations", now, map[string]float64{
		"completed": m["completed_operations"], "initiated": m["initiated_operations"]})
	_ = reg.Collect("openldap.operations_by_type", now, map[string]float64{
		"bind": m["completed_bind_operations"], "search": m["completed_search_operations"],
		"unbind": m["completed_unbind_operations"], "add": m["completed_add_operations"],
		"delete": m["completed_delete_operations"], "modify": m["completed_modify_operations"],
		"compare": m["completed_compare_operations"]})
	_ = reg.Collect("openldap.waiters", now, map[string]float64{"read": m["read_waiters"], "write": m["write_waiters"]})
	return nil
}

func (o *openldapCollector) monitor(ctx context.Context) (map[string]float64, error) {
	run := o.run
	if run == nil {
		run = execRun(o.cfg.Timeout)
	}
	args := []string{"-x", "-LLL", "-o", "ldif-wrap=no", "-H", o.cfg.URL, "-b", "cn=Monitor", "-s", "sub", "+", "*"}
	if o.cfg.BindDN != "" {
		args = append([]string{"-D", o.cfg.BindDN, "-w", o.cfg.Password}, args...)
	}
	out, err := run(ctx, o.cfg.Command, args...)
	if err != nil {
		return nil, fmt.Errorf("ldapsearch: %w", err)
	}
	m := parseOpenLDAPMonitor(out)
	if len(m) == 0 {
		return nil, fmt.Errorf("openldap: no monitor counters")
	}
	return m, nil
}

func parseOpenLDAPMonitor(b []byte) map[string]float64 {
	out := map[string]float64{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	var dn string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			dn = ""
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "dn:") {
			dn = strings.ToLower(strings.TrimSpace(line[3:]))
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		val := firstFloat(v)
		switch key {
		case "monitorcounter":
			switch {
			case strings.Contains(dn, "cn=current,cn=connections"):
				out["current_connections"] = val
			case strings.Contains(dn, "cn=total,cn=connections"):
				out["total_connections"] = val
			case strings.Contains(dn, "cn=bytes"):
				out["bytes_sent"] = val
			case strings.Contains(dn, "cn=entries"):
				out["entries_sent"] = val
			case strings.Contains(dn, "cn=referrals"):
				out["referrals_sent"] = val
			case strings.Contains(dn, "cn=read,cn=waiters"):
				out["read_waiters"] = val
			case strings.Contains(dn, "cn=write,cn=waiters"):
				out["write_waiters"] = val
			}
		case "monitoropcompleted", "monitoropinitiated":
			kind := "completed"
			if key == "monitoropinitiated" {
				kind = "initiated"
			}
			if name := openldapOpName(dn); name != "" {
				out[kind+"_"+name+"_operations"] = val
			} else if strings.Count(dn, "cn=") == 2 && strings.Contains(dn, "cn=operations,cn=monitor") {
				out[kind+"_operations"] = val
			}
		}
	}
	return out
}

func openldapOpName(dn string) string {
	for _, op := range []string{"bind", "search", "unbind", "add", "delete", "modify", "compare"} {
		if strings.Contains(dn, "cn="+op+",cn=operations") {
			return op
		}
	}
	return ""
}
