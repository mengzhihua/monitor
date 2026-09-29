package collect

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/preprocess"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

// telnetcheck opens a TCP session, optionally writes one line, and reads until
// expect matches or the timeout. It does not invoke a shell.
type telnetConfig struct {
	Timeout time.Duration `yaml:"timeout"`
	Jobs    []telnetJob   `yaml:"jobs"`
}

type telnetJob struct {
	Name    string            `yaml:"name"`
	Address string            `yaml:"address"`
	Send    string            `yaml:"send"`
	Expect  string            `yaml:"expect"`
	Steps   []preprocess.Step `yaml:"preprocess"`
}

type telnetCollector struct {
	cfg  telnetConfig
	prev map[string]float64
	dial func(ctx context.Context, address string) (net.Conn, error)
}

func init() { Register("telnet", func() Collector { return &telnetCollector{} }) }

func (t *telnetCollector) Name() string { return "telnet" }

func (t *telnetCollector) Configure(decode func(v any) error) error {
	if err := decode(&t.cfg); err != nil {
		return err
	}
	if t.cfg.Timeout <= 0 {
		t.cfg.Timeout = 5 * time.Second
	}
	for _, j := range t.cfg.Jobs {
		if !validItemName(j.Name) || j.Address == "" || strings.ContainsAny(j.Address+j.Send, "\n\r") {
			return fmt.Errorf("telnet: job %q needs a name and address", j.Name)
		}
		if _, _, err := net.SplitHostPort(j.Address); err != nil {
			return fmt.Errorf("telnet %s: address must be host:port", j.Name)
		}
	}
	return nil
}

func (t *telnetCollector) Init(reg *registry.Registry) error {
	if len(t.cfg.Jobs) == 0 {
		return fmt.Errorf("telnet: no jobs")
	}
	t.prev = map[string]float64{}
	for _, j := range t.cfg.Jobs {
		reg.AddChart(&registry.Chart{ID: "telnet." + sanitizeID(j.Name), Context: "telnet.value", Title: "Telnet " + j.Name,
			Units: "value", Family: "telnet", Plugin: "telnet", Module: "telnet", Priority: 56250,
			Labels: map[string]string{"address": j.Address}, Dimensions: []*registry.Dimension{{ID: "value"}}})
	}
	return nil
}

func (t *telnetCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	for _, j := range t.cfg.Jobs {
		raw, err := t.session(ctx, j)
		prev := math.NaN()
		if v, ok := t.prev[j.Name]; ok {
			prev = v
		}
		steps := j.Steps
		if len(steps) == 0 && j.Expect != "" {
			steps = []preprocess.Step{{Type: "bool", Pattern: j.Expect}}
		}
		value, next, perr := preprocess.Apply(steps, raw, prev)
		if err != nil || perr != nil || math.IsNaN(value) {
			continue
		}
		t.prev[j.Name] = next
		_ = reg.Collect("telnet."+sanitizeID(j.Name), now, map[string]float64{"value": value})
	}
	return nil
}

func (t *telnetCollector) session(ctx context.Context, j telnetJob) (string, error) {
	dial := t.dial
	if dial == nil {
		d := net.Dialer{Timeout: t.cfg.Timeout}
		dial = func(ctx context.Context, address string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp", address)
		}
	}
	conn, err := dial(ctx, j.Address)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	deadline := time.Now().Add(t.cfg.Timeout)
	_ = conn.SetDeadline(deadline)
	if j.Send != "" {
		if _, err := fmt.Fprintf(conn, "%s\r\n", j.Send); err != nil {
			return "", err
		}
	}
	var b strings.Builder
	r := bufio.NewReader(conn)
	var expect *regexp.Regexp
	if j.Expect != "" {
		expect, err = regexp.Compile(j.Expect)
		if err != nil {
			return "", err
		}
	}
	for b.Len() < 64<<10 {
		line, err := r.ReadString('\n')
		b.WriteString(stripTelnet(line))
		if expect != nil && expect.MatchString(b.String()) {
			break
		}
		if err != nil {
			break
		}
	}
	return b.String(), nil
}

func stripTelnet(s string) string {
	var b strings.Builder
	raw := []byte(s)
	for i := 0; i < len(raw); i++ {
		if raw[i] != 255 || i+1 >= len(raw) {
			b.WriteByte(raw[i])
			continue
		}
		cmd := raw[i+1]
		i++
		if cmd == 255 {
			b.WriteByte(255)
			continue
		}
		if cmd >= 251 && cmd <= 254 && i+1 < len(raw) {
			i++
		}
	}
	return b.String()
}
