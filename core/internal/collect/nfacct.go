package collect

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// nfacctConfig is collectors.modules.nfacct (Netdata nfacct.plugin via `nfacct list`).
type nfacctConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type nfacctCollector struct {
	cfg  nfacctConfig
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	seen map[string]bool
}

func init() {
	Register("nfacct", func() Collector { return &nfacctCollector{} })
}

func (n *nfacctCollector) Name() string { return "nfacct" }

func (n *nfacctCollector) Configure(decode func(v any) error) error {
	if err := decode(&n.cfg); err != nil {
		return err
	}
	if n.cfg.Command == "" {
		n.cfg.Command = "nfacct"
	}
	if n.cfg.Timeout <= 0 {
		n.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (n *nfacctCollector) Init(reg *registry.Registry) error {
	if n.cfg.Command == "" {
		if err := n.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if n.run == nil {
		n.run = execRun(n.cfg.Timeout)
	}
	raw, err := n.run(context.Background(), n.cfg.Command, "list")
	if err != nil {
		return fmt.Errorf("nfacct: unavailable: %w", err)
	}
	if len(parseNfacct(string(raw))) == 0 {
		return fmt.Errorf("nfacct: no objects")
	}
	n.seen = map[string]bool{}
	return nil
}

func (n *nfacctCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	raw, err := n.run(ctx, n.cfg.Command, "list")
	if err != nil {
		return err
	}
	for _, o := range parseNfacct(string(raw)) {
		id := sanitizeID(o.Name)
		pkts := "netfilter.nfacct_packets." + id
		bytes := "netfilter.nfacct_bytes." + id
		if !n.seen[pkts] {
			n.seen[pkts] = true
			ch := sysChart(pkts, "nfacct", "nfacct packets "+o.Name, "packets/s", 8070, incDim("packets"))
			ch.Plugin, ch.Module = "nfacct", "nfacct"
			reg.AddChart(ch)
			b := sysChart(bytes, "nfacct", "nfacct bytes "+o.Name, "bytes/s", 8071, incDim("bytes"))
			b.Plugin, b.Module = "nfacct", "nfacct"
			reg.AddChart(b)
		}
		_ = reg.Collect(pkts, now, map[string]float64{"packets": o.Packets})
		_ = reg.Collect(bytes, now, map[string]float64{"bytes": o.Bytes})
	}
	return nil
}

type nfacctObj struct {
	Name           string
	Packets, Bytes float64
}

func parseNfacct(s string) []nfacctObj {
	var out []nfacctObj
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name := ""
		if i := strings.LastIndex(line, "="); i >= 0 {
			name = strings.Trim(strings.TrimSpace(line[i+1:]), "; ")
		}
		if name == "" {
			continue
		}
		o := nfacctObj{Name: name}
		f := strings.Fields(strings.ReplaceAll(strings.ReplaceAll(line, ",", " "), "=", " "))
		for i := 0; i+1 < len(f); i++ {
			switch strings.Trim(f[i], "{}") {
			case "pkts", "packets":
				o.Packets = firstFloat(f[i+1])
			case "bytes":
				o.Bytes = firstFloat(f[i+1])
			}
		}
		out = append(out, o)
	}
	return out
}
