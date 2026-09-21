package collect

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// postfixConfig is collectors.modules.postfix (postqueue -p).
type postfixConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type postfixCollector struct {
	cfg postfixConfig
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func init() {
	Register("postfix", func() Collector { return &postfixCollector{} })
}

func (p *postfixCollector) Name() string { return "postfix" }

func (p *postfixCollector) Configure(decode func(v any) error) error {
	if err := decode(&p.cfg); err != nil {
		return err
	}
	if p.cfg.Command == "" {
		p.cfg.Command = "postqueue"
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (p *postfixCollector) Init(reg *registry.Registry) error {
	if p.cfg.Command == "" {
		if err := p.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := p.queue(context.Background()); err != nil {
		return err
	}
	for _, c := range []*registry.Chart{
		{ID: "postfix.qemails", Title: "Postfix Queue Emails", Units: "emails", Priority: 52000,
			Dimensions: []*registry.Dimension{{ID: "emails"}}},
		{ID: "postfix.qsize", Title: "Postfix Queue Size", Units: "KiB", Type: registry.Area, Priority: 52010,
			Dimensions: []*registry.Dimension{{ID: "size"}}},
	} {
		c.Family, c.Plugin, c.Module = "postfix", "postfix", "postfix"
		reg.AddChart(c)
	}
	return nil
}

func (p *postfixCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	s, err := p.queue(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("postfix.qemails", now, map[string]float64{"emails": s.emails})
	_ = reg.Collect("postfix.qsize", now, map[string]float64{"size": s.sizeKiB})
	return nil
}

type postfixQueue struct{ emails, sizeKiB float64 }

func (p *postfixCollector) queue(ctx context.Context) (postfixQueue, error) {
	run := p.run
	if run == nil {
		run = execRun(p.cfg.Timeout)
	}
	out, err := run(ctx, p.cfg.Command, "-p")
	if err != nil {
		return postfixQueue{}, fmt.Errorf("postqueue: %w", err)
	}
	return parsePostqueue(out)
}

func parsePostqueue(b []byte) (postfixQueue, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return postfixQueue{}, fmt.Errorf("postqueue: empty")
	}
	var last string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			last = line
		}
	}
	if last == "Mail queue is empty" {
		return postfixQueue{}, nil
	}
	// -- 3 Kbytes in 3 Requests.
	parts := strings.Fields(last)
	if len(parts) < 5 {
		return postfixQueue{}, fmt.Errorf("postqueue: unexpected %q", last)
	}
	size, err := strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return postfixQueue{}, fmt.Errorf("postqueue: unexpected %q", last)
	}
	n, err := strconv.ParseFloat(parts[4], 64)
	if err != nil {
		return postfixQueue{}, fmt.Errorf("postqueue: unexpected %q", last)
	}
	return postfixQueue{emails: n, sizeKiB: size}, nil
}
