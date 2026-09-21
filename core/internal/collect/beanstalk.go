package collect

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// beanstalkConfig is collectors.modules.beanstalk (YAML stats :11300).
type beanstalkConfig struct {
	Address string        `yaml:"address"`
	Timeout time.Duration `yaml:"timeout"`
}

type beanstalkCollector struct {
	cfg  beanstalkConfig
	dial func(ctx context.Context, network, address string) (net.Conn, error)
}

func init() {
	Register("beanstalk", func() Collector { return &beanstalkCollector{} })
}

func (b *beanstalkCollector) Name() string { return "beanstalk" }

func (b *beanstalkCollector) Configure(decode func(v any) error) error {
	if err := decode(&b.cfg); err != nil {
		return err
	}
	if b.cfg.Address == "" {
		b.cfg.Address = "127.0.0.1:11300"
	}
	if b.cfg.Timeout <= 0 {
		b.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (b *beanstalkCollector) Init(reg *registry.Registry) error {
	if b.cfg.Address == "" {
		if err := b.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := b.stats(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "beanstalk.current_jobs", Title: "Current Jobs", Units: "jobs", Type: registry.Stacked, Priority: 56800,
			Dimensions: []*registry.Dimension{{ID: "ready"}, {ID: "buried"}, {ID: "urgent"}, {ID: "delayed"}, {ID: "reserved"}}},
		{ID: "beanstalk.jobs_rate", Title: "Jobs Rate", Units: "jobs/s", Priority: 56810,
			Dimensions: []*registry.Dimension{{ID: "created", Algorithm: inc}}},
		{ID: "beanstalk.jobs_timeouts", Title: "Timed Out Jobs", Units: "jobs/s", Priority: 56820,
			Dimensions: []*registry.Dimension{{ID: "timeouts", Algorithm: inc}}},
		{ID: "beanstalk.current_tubes", Title: "Current Tubes", Units: "tubes", Priority: 56830,
			Dimensions: []*registry.Dimension{{ID: "tubes"}}},
		{ID: "beanstalk.current_connections", Title: "Current Connections", Units: "connections", Priority: 56840,
			Dimensions: []*registry.Dimension{{ID: "open"}, {ID: "producers"}, {ID: "workers"}, {ID: "waiting"}}},
		{ID: "beanstalk.connections_rate", Title: "Connections Rate", Units: "connections/s", Priority: 56850,
			Dimensions: []*registry.Dimension{{ID: "created", Algorithm: inc}}},
		{ID: "beanstalk.uptime", Title: "Uptime", Units: "seconds", Priority: 56860,
			Dimensions: []*registry.Dimension{{ID: "uptime"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "beanstalk", "beanstalk", "beanstalk"
		reg.AddChart(ch)
	}
	return nil
}

func (b *beanstalkCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	s, err := b.stats(ctx)
	if err != nil {
		return err
	}
	g := func(k string) float64 { return firstFloat(s[k]) }
	_ = reg.Collect("beanstalk.current_jobs", now, map[string]float64{
		"ready": g("current-jobs-ready"), "buried": g("current-jobs-buried"),
		"urgent": g("current-jobs-urgent"), "delayed": g("current-jobs-delayed"),
		"reserved": g("current-jobs-reserved"),
	})
	_ = reg.Collect("beanstalk.jobs_rate", now, map[string]float64{"created": g("total-jobs")})
	_ = reg.Collect("beanstalk.jobs_timeouts", now, map[string]float64{"timeouts": g("job-timeouts")})
	_ = reg.Collect("beanstalk.current_tubes", now, map[string]float64{"tubes": g("current-tubes")})
	_ = reg.Collect("beanstalk.current_connections", now, map[string]float64{
		"open": g("current-connections"), "producers": g("current-producers"),
		"workers": g("current-workers"), "waiting": g("current-waiting"),
	})
	_ = reg.Collect("beanstalk.connections_rate", now, map[string]float64{"created": g("total-connections")})
	_ = reg.Collect("beanstalk.uptime", now, map[string]float64{"uptime": g("uptime")})
	return nil
}

func (b *beanstalkCollector) stats(ctx context.Context) (map[string]string, error) {
	dial := b.dial
	if dial == nil {
		d := net.Dialer{Timeout: b.cfg.Timeout}
		dial = d.DialContext
	}
	conn, err := dial(ctx, "tcp", b.cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("beanstalk: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(b.cfg.Timeout))
	if _, err := io.WriteString(conn, "stats\r\n"); err != nil {
		return nil, err
	}
	r := bufio.NewReader(conn)
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("beanstalk: %w", err)
	}
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "OK") {
		return nil, fmt.Errorf("beanstalk: %s", line)
	}
	n := 0
	if fields := strings.Fields(line); len(fields) >= 2 {
		n, _ = strconv.Atoi(fields[1])
	}
	body := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, err
		}
	} else {
		body, _ = io.ReadAll(io.LimitReader(r, 1<<20))
	}
	out := map[string]string{}
	for _, l := range strings.Split(string(body), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "---") {
			continue
		}
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("beanstalk: no stats")
	}
	return out, nil
}
