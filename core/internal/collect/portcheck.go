package collect

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

type portcheckConfig struct {
	Jobs    []portJob     `yaml:"jobs"`
	Timeout time.Duration `yaml:"timeout"`
}

type portJob struct {
	Name    string        `yaml:"name"`
	Host    string        `yaml:"host"`
	Port    int           `yaml:"port"`
	Timeout time.Duration `yaml:"timeout"`
}

type portcheckCollector struct {
	cfg portcheckConfig
}

func init() {
	Register("portcheck", func() Collector { return &portcheckCollector{} })
}

func (p *portcheckCollector) Name() string { return "portcheck" }

func (p *portcheckCollector) Configure(decode func(v any) error) error {
	if err := decode(&p.cfg); err != nil {
		return err
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (p *portcheckCollector) Init(reg *registry.Registry) error {
	if p.cfg.Timeout <= 0 {
		if err := p.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if len(p.cfg.Jobs) == 0 {
		return fmt.Errorf("no jobs configured")
	}
	for i := range p.cfg.Jobs {
		j := &p.cfg.Jobs[i]
		if j.Host == "" {
			j.Host = "127.0.0.1"
		}
		if j.Port <= 0 {
			return fmt.Errorf("portcheck job %q: port required", j.Name)
		}
		if j.Name == "" {
			j.Name = fmt.Sprintf("%s_%d", j.Host, j.Port)
		}
		if j.Timeout <= 0 {
			j.Timeout = p.cfg.Timeout
		}
		id := sanitizeID(j.Name)
		lbl := map[string]string{"job": j.Name, "host": j.Host, "port": fmt.Sprint(j.Port)}
		reg.AddChart(&registry.Chart{ID: "portcheck.status." + id, Context: "portcheck.status", Family: j.Name,
			Title: "TCP port check status", Units: "boolean", Priority: 51000, Plugin: "portcheck", Module: "portcheck",
			Labels: lbl, Dimensions: []*registry.Dimension{{ID: "success"}, {ID: "failure"}}})
		reg.AddChart(&registry.Chart{ID: "portcheck.latency." + id, Context: "portcheck.latency", Family: j.Name,
			Title: "TCP port check latency", Units: "ms", Priority: 51001, Plugin: "portcheck", Module: "portcheck",
			Labels: lbl, Dimensions: []*registry.Dimension{{ID: "time"}}})
	}
	return nil
}

func (p *portcheckCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	for _, j := range p.cfg.Jobs {
		id := sanitizeID(j.Name)
		d := net.Dialer{Timeout: j.Timeout}
		start := time.Now()
		cctx, cancel := context.WithTimeout(ctx, j.Timeout)
		conn, err := d.DialContext(cctx, "tcp", net.JoinHostPort(j.Host, fmt.Sprint(j.Port)))
		cancel()
		elapsed := float64(time.Since(start).Microseconds()) / 1000
		success, failure := 1.0, 0.0
		if err != nil {
			success, failure = 0, 1
		} else {
			_ = conn.Close()
		}
		_ = reg.Collect("portcheck.status."+id, now, map[string]float64{"success": success, "failure": failure})
		_ = reg.Collect("portcheck.latency."+id, now, map[string]float64{"time": elapsed})
	}
	return nil
}
