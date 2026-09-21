package collect

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// dnsqueryConfig is collectors.modules.dnsquery (Netdata go.d dns_query).
type dnsqueryConfig struct {
	Jobs    []dnsJob      `yaml:"jobs"`
	Timeout time.Duration `yaml:"timeout"`
}

type dnsJob struct {
	Name      string        `yaml:"name"`
	Server    string        `yaml:"server"` // host:port, default system resolver
	Record    string        `yaml:"record"`
	QueryType string        `yaml:"query_type"` // unused; net.Resolver looks up A/AAAA
	Timeout   time.Duration `yaml:"timeout"`
}

type dnsqueryCollector struct {
	cfg dnsqueryConfig
}

func init() {
	Register("dnsquery", func() Collector { return &dnsqueryCollector{} })
}

func (d *dnsqueryCollector) Name() string { return "dnsquery" }

func (d *dnsqueryCollector) Configure(decode func(v any) error) error {
	if err := decode(&d.cfg); err != nil {
		return err
	}
	if d.cfg.Timeout <= 0 {
		d.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (d *dnsqueryCollector) Init(reg *registry.Registry) error {
	if d.cfg.Timeout <= 0 {
		if err := d.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if len(d.cfg.Jobs) == 0 {
		return fmt.Errorf("no jobs configured")
	}
	for i := range d.cfg.Jobs {
		j := &d.cfg.Jobs[i]
		if j.Name == "" {
			j.Name = fmt.Sprintf("job%d", i)
		}
		if j.Record == "" {
			return fmt.Errorf("dnsquery job %q: empty record", j.Name)
		}
		if j.Timeout <= 0 {
			j.Timeout = d.cfg.Timeout
		}
		id := "dnsquery." + j.Name
		reg.AddChart(&registry.Chart{ID: id + ".query_time", Context: "dns_query.query_time",
			Family: "dnsquery", Title: "DNS query time " + j.Name, Units: "ms",
			Priority: 49100, Plugin: "dnsquery", Module: "dnsquery", Labels: map[string]string{"instance": j.Name},
			Dimensions: []*registry.Dimension{{ID: "query_time"}}})
		reg.AddChart(&registry.Chart{ID: id + ".query_status", Context: "dns_query.query_status",
			Family: "dnsquery", Title: "DNS query status " + j.Name, Units: "boolean",
			Priority: 49110, Plugin: "dnsquery", Module: "dnsquery", Labels: map[string]string{"instance": j.Name},
			Dimensions: []*registry.Dimension{{ID: "success"}, {ID: "failure"}}})
	}
	return nil
}

func (d *dnsqueryCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	var last error
	for _, j := range d.cfg.Jobs {
		ms, err := dnsLookup(ctx, j)
		id := "dnsquery." + j.Name
		ok := 1.0
		if err != nil {
			ok = 0
			last = err
		}
		_ = reg.Collect(id+".query_time", now, map[string]float64{"query_time": ms})
		_ = reg.Collect(id+".query_status", now, map[string]float64{"success": ok, "failure": 1 - ok})
	}
	return last
}

func dnsLookup(ctx context.Context, j dnsJob) (float64, error) {
	r := &net.Resolver{PreferGo: true}
	if j.Server != "" {
		server := j.Server
		if _, _, err := net.SplitHostPort(server); err != nil {
			server = net.JoinHostPort(server, "53")
		}
		r.Dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: j.Timeout}
			return d.DialContext(ctx, "udp", server)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, j.Timeout)
	defer cancel()
	start := time.Now()
	_, err := r.LookupHost(cctx, j.Record)
	ms := float64(time.Since(start).Milliseconds())
	return ms, err
}
