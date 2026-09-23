package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// vcsaConfig is collectors.modules.vcsa (vCenter Server Appliance REST health).
type vcsaConfig struct {
	TLS      CollectorTLS  `yaml:"tls"`
	URL      string        `yaml:"url"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type vcsaCollector struct {
	cfg     vcsaConfig
	client  *http.Client
	url     string
	session string
}

func init() {
	Register("vcsa", func() Collector { return &vcsaCollector{} })
}

func (v *vcsaCollector) Name() string { return "vcsa" }

func (v *vcsaCollector) Configure(decode func(v any) error) error {
	if err := decode(&v.cfg); err != nil {
		return err
	}
	if v.cfg.Timeout <= 0 {
		v.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (v *vcsaCollector) Init(reg *registry.Registry) error {
	if v.cfg.Timeout <= 0 {
		if err := v.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if strings.TrimSpace(v.cfg.User) == "" {
		return fmt.Errorf("vcsa: no credentials")
	}
	client, tlsErr := collectorHTTPClient(v.cfg.Timeout, v.cfg.TLS)
	if tlsErr != nil {
		return tlsErr
	}
	v.client = client
	base := strings.TrimRight(v.cfg.URL, "/")
	if base == "" {
		base = "https://127.0.0.1:5480"
	}
	v.url = base
	if err := v.login(context.Background()); err != nil {
		return err
	}
	if _, err := v.health(context.Background()); err != nil {
		return err
	}
	for _, spec := range vcsaHealthSpecs {
		dims := make([]*registry.Dimension, 0, len(spec.statuses)+1)
		for _, st := range spec.statuses {
			dims = append(dims, &registry.Dimension{ID: st})
		}
		dims = append(dims, &registry.Dimension{ID: "unknown"})
		ch := &registry.Chart{
			ID: "vcsa." + spec.id, Context: "vcsa." + spec.id, Title: spec.title,
			Units: "status", Family: spec.family, Priority: spec.prio, Dimensions: dims,
		}
		ch.Plugin, ch.Module = "vcsa", "vcsa"
		reg.AddChart(ch)
	}
	return nil
}

func (v *vcsaCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	st, err := v.health(ctx)
	if err != nil {
		_ = v.login(ctx)
		st, err = v.health(ctx)
		if err != nil {
			return err
		}
	}
	for _, spec := range vcsaHealthSpecs {
		vals := map[string]float64{"unknown": 1}
		for _, s := range spec.statuses {
			vals[s] = 0
		}
		cur := strings.ToLower(st[spec.prefix])
		if _, ok := vals[cur]; ok && cur != "unknown" {
			vals[cur] = 1
			vals["unknown"] = 0
		}
		_ = reg.Collect("vcsa."+spec.id, now, vals)
	}
	return nil
}

type vcsaHealthSpec struct {
	id, title, family, prefix string
	prio                      int
	statuses                  []string
}

var vcsaHealthSpecs = []vcsaHealthSpec{
	{"system_health_status", "VCSA Overall System health status", "system", "system", 62000, []string{"green", "red", "yellow", "orange", "gray"}},
	{"applmgmt_health_status", "VCSA Appliance Management Service (applmgmt) health status", "appliance mgmt service", "applmgmt", 62010, []string{"green", "red", "yellow", "orange", "gray"}},
	{"load_health_status", "VCSA Load health status", "load", "load", 62020, []string{"green", "red", "yellow", "orange", "gray"}},
	{"mem_health_status", "VCSA Memory health status", "mem", "mem", 62030, []string{"green", "red", "yellow", "orange", "gray"}},
	{"swap_health_status", "VCSA Swap health status", "swap", "swap", 62040, []string{"green", "red", "yellow", "orange", "gray"}},
	{"database_storage_health_status", "VCSA Database Storage health status", "db storage", "database_storage", 62050, []string{"green", "red", "yellow", "orange", "gray"}},
	{"storage_health_status", "VCSA Storage health status", "storage", "storage", 62060, []string{"green", "red", "yellow", "orange", "gray"}},
	{"software_packages_health_status", "VCSA Software Updates health status", "software packages", "software_packages", 62070, []string{"green", "red", "orange", "gray"}},
}

func (v *vcsaCollector) login(ctx context.Context) error {
	b, err := httpDo(ctx, v.client, http.MethodPost, v.url+"/rest/com/vmware/cis/session", "", "", nil, v.cfg.User, v.cfg.Password)
	if err != nil {
		return fmt.Errorf("vcsa: %w", err)
	}
	m, _ := jsonMap(b)
	tok := nestString(m, "value")
	if tok == "" {
		tok = nestString(m, "session_id")
	}
	if tok == "" {
		return fmt.Errorf("vcsa: no session")
	}
	v.session = tok
	return nil
}

func (v *vcsaCollector) health(ctx context.Context) (map[string]string, error) {
	paths := []struct{ key, path string }{
		{"system", "/rest/appliance/health/system"},
		{"applmgmt", "/rest/appliance/health/applmgmt"},
		{"load", "/rest/appliance/health/load"},
		{"mem", "/rest/appliance/health/mem"},
		{"swap", "/rest/appliance/health/swap"},
		{"database_storage", "/rest/appliance/health/database-storage"},
		{"storage", "/rest/appliance/health/storage"},
		{"software_packages", "/rest/appliance/health/software-packages"},
	}
	out := map[string]string{}
	hdr := map[string]string{"vmware-api-session-id": v.session}
	var last error
	ok := 0
	for _, p := range paths {
		b, err := httpDo(ctx, v.client, http.MethodGet, v.url+p.path, "", "", hdr, "", "")
		if err != nil {
			last = err
			continue
		}
		m, _ := jsonMap(b)
		val := nestString(m, "value")
		if val == "" {
			val = strings.TrimSpace(string(b))
		}
		if val == "" {
			last = fmt.Errorf("vcsa: empty %s", p.key)
			continue
		}
		out[p.key] = val
		ok++
	}
	if ok == 0 {
		if last == nil {
			last = fmt.Errorf("vcsa: no health")
		}
		return nil, last
	}
	return out, nil
}
