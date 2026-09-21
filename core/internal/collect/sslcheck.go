package collect

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// sslcheckConfig is collectors.modules.sslcheck (Netdata go.d sslcheck).
type sslcheckConfig struct {
	Jobs    []sslJob      `yaml:"jobs"`
	Timeout time.Duration `yaml:"timeout"`
}

type sslJob struct {
	Name    string        `yaml:"name"`
	Target  string        `yaml:"target"` // host:port (default :443)
	Timeout time.Duration `yaml:"timeout"`
	Server  string        `yaml:"server_name"`
}

type sslcheckCollector struct {
	cfg sslcheckConfig
}

func init() {
	Register("sslcheck", func() Collector { return &sslcheckCollector{} })
}

func (s *sslcheckCollector) Name() string { return "sslcheck" }

func (s *sslcheckCollector) Configure(decode func(v any) error) error {
	if err := decode(&s.cfg); err != nil {
		return err
	}
	if s.cfg.Timeout <= 0 {
		s.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (s *sslcheckCollector) Init(reg *registry.Registry) error {
	if s.cfg.Timeout <= 0 {
		if err := s.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if len(s.cfg.Jobs) == 0 {
		return fmt.Errorf("no jobs configured")
	}
	for i := range s.cfg.Jobs {
		j := &s.cfg.Jobs[i]
		if j.Name == "" {
			j.Name = fmt.Sprintf("job%d", i)
		}
		if j.Target == "" {
			return fmt.Errorf("sslcheck job %q: empty target", j.Name)
		}
		if !strings.Contains(j.Target, ":") {
			j.Target += ":443"
		}
		if j.Timeout <= 0 {
			j.Timeout = s.cfg.Timeout
		}
		if j.Server == "" {
			j.Server, _, _ = net.SplitHostPort(j.Target)
		}
		id := "sslcheck." + j.Name
		reg.AddChart(&registry.Chart{ID: id + ".time_until_expiration", Context: "sslcheck.time_until_expiration",
			Family: "sslcheck", Title: "TLS certificate days until expiration " + j.Name, Units: "days",
			Priority: 49000, Plugin: "sslcheck", Module: "sslcheck", Labels: map[string]string{"instance": j.Name},
			Dimensions: []*registry.Dimension{{ID: "days"}}})
		reg.AddChart(&registry.Chart{ID: id + ".run_time", Context: "sslcheck.run_time",
			Family: "sslcheck", Title: "TLS handshake time " + j.Name, Units: "ms",
			Priority: 49010, Plugin: "sslcheck", Module: "sslcheck", Labels: map[string]string{"instance": j.Name},
			Dimensions: []*registry.Dimension{{ID: "time"}}})
	}
	return nil
}

func (s *sslcheckCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	var last error
	for _, j := range s.cfg.Jobs {
		days, ms, err := tlsDays(ctx, j)
		id := "sslcheck." + j.Name
		if err != nil {
			last = err
			_ = reg.Collect(id+".time_until_expiration", now, map[string]float64{"days": 0})
			_ = reg.Collect(id+".run_time", now, map[string]float64{"time": ms})
			continue
		}
		_ = reg.Collect(id+".time_until_expiration", now, map[string]float64{"days": days})
		_ = reg.Collect(id+".run_time", now, map[string]float64{"time": ms})
	}
	return last
}

func tlsDays(ctx context.Context, j sslJob) (days, ms float64, err error) {
	start := time.Now()
	d := net.Dialer{Timeout: j.Timeout}
	raw, err := d.DialContext(ctx, "tcp", j.Target)
	ms = float64(time.Since(start).Milliseconds())
	if err != nil {
		return 0, ms, err
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(j.Timeout))
	c := tls.Client(raw, &tls.Config{ServerName: j.Server, MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}) //nolint:gosec // we inspect the cert ourselves
	if err := c.HandshakeContext(ctx); err != nil {
		ms = float64(time.Since(start).Milliseconds())
		return 0, ms, err
	}
	ms = float64(time.Since(start).Milliseconds())
	state := c.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return 0, ms, fmt.Errorf("no peer certificate")
	}
	left := time.Until(state.PeerCertificates[0].NotAfter).Hours() / 24
	return left, ms, nil
}
