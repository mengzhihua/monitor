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

// gearmanConfig is collectors.modules.gearman (admin status :4730).
type gearmanConfig struct {
	Address string        `yaml:"address"`
	Timeout time.Duration `yaml:"timeout"`
}

type gearmanCollector struct {
	cfg  gearmanConfig
	dial func(ctx context.Context, network, address string) (net.Conn, error)
}

func init() {
	Register("gearman", func() Collector { return &gearmanCollector{} })
}

func (g *gearmanCollector) Name() string { return "gearman" }

func (g *gearmanCollector) Configure(decode func(v any) error) error {
	if err := decode(&g.cfg); err != nil {
		return err
	}
	if g.cfg.Address == "" {
		g.cfg.Address = "127.0.0.1:4730"
	}
	if g.cfg.Timeout <= 0 {
		g.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (g *gearmanCollector) Init(reg *registry.Registry) error {
	if g.cfg.Address == "" {
		if err := g.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := g.status(context.Background()); err != nil {
		return err
	}
	for _, ch := range []*registry.Chart{
		{ID: "gearman.queued_jobs_activity", Title: "Jobs Activity", Units: "jobs", Type: registry.Stacked, Priority: 57700,
			Dimensions: []*registry.Dimension{{ID: "running"}, {ID: "waiting"}}},
		{ID: "gearman.queued_jobs_priority", Title: "Jobs Priority", Units: "jobs", Type: registry.Stacked, Priority: 57710,
			Dimensions: []*registry.Dimension{{ID: "high"}, {ID: "normal"}, {ID: "low"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "gearman", "gearman", "gearman"
		reg.AddChart(ch)
	}
	return nil
}

func (g *gearmanCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	st, err := g.status(ctx)
	if err != nil {
		return err
	}
	run, wait := parseGearmanStatus(st)
	_ = reg.Collect("gearman.queued_jobs_activity", now, map[string]float64{"running": run, "waiting": wait})
	prio, err := g.priority(ctx)
	if err == nil {
		h, n, l := parseGearmanPriority(prio)
		_ = reg.Collect("gearman.queued_jobs_priority", now, map[string]float64{"high": h, "normal": n, "low": l})
	} else {
		_ = reg.Collect("gearman.queued_jobs_priority", now, map[string]float64{"high": 0, "normal": run + wait, "low": 0})
	}
	return nil
}

func (g *gearmanCollector) status(ctx context.Context) (string, error) {
	return g.cmd(ctx, "status\n")
}

func (g *gearmanCollector) priority(ctx context.Context) (string, error) {
	return g.cmd(ctx, "prioritystatus\n")
}

func (g *gearmanCollector) cmd(ctx context.Context, cmd string) (string, error) {
	dial := g.dial
	if dial == nil {
		d := net.Dialer{Timeout: g.cfg.Timeout}
		dial = d.DialContext
	}
	conn, err := dial(ctx, "tcp", g.cfg.Address)
	if err != nil {
		return "", fmt.Errorf("gearman: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(g.cfg.Timeout))
	if _, err := io.WriteString(conn, cmd); err != nil {
		return "", err
	}
	var b strings.Builder
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		line := sc.Text()
		b.WriteString(line)
		b.WriteByte('\n')
		if strings.TrimSpace(line) == "." {
			break
		}
	}
	out := b.String()
	if !strings.Contains(out, ".") {
		return "", fmt.Errorf("gearman: unexpected status")
	}
	return out, nil
}

func parseGearmanStatus(s string) (running, waiting float64) {
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "." || line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 4 {
			continue
		}
		metrics := parts[len(parts)-3:]
		queued, _ := strconv.ParseFloat(metrics[0], 64)
		run, _ := strconv.ParseFloat(metrics[1], 64)
		running += run
		if queued > run {
			waiting += queued - run
		}
	}
	return running, waiting
}

func parseGearmanPriority(s string) (high, normal, low float64) {
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "." || line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 5 {
			continue
		}
		m := parts[len(parts)-4:]
		h, _ := strconv.ParseFloat(m[0], 64)
		n, _ := strconv.ParseFloat(m[1], 64)
		l, _ := strconv.ParseFloat(m[2], 64)
		high += h
		normal += n
		low += l
	}
	return high, normal, low
}
