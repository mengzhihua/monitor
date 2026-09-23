package collect

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// nftablesConfig is collectors.modules.nftables (Netdata nfacct/nftables via nft).
type nftablesConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type nftablesCollector struct {
	cfg     nftablesConfig
	run     func(ctx context.Context, name string, args ...string) ([]byte, error)
	seen    map[string]bool
	netlink bool
	last    time.Time
}

const nftablesEvery = 10 * time.Second

func init() {
	Register("nftables", func() Collector { return &nftablesCollector{} })
}

func (n *nftablesCollector) Name() string { return "nftables" }

func (n *nftablesCollector) Configure(decode func(v any) error) error {
	if err := decode(&n.cfg); err != nil {
		return err
	}
	if n.cfg.Command == "" {
		n.cfg.Command = "nft"
	}
	if n.cfg.Timeout <= 0 {
		n.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (n *nftablesCollector) Init(reg *registry.Registry) error {
	if n.cfg.Command == "" {
		if err := n.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if n.run == nil {
		if objs, ok := readNftCountersNetlink(); ok && len(objs) > 0 {
			n.netlink = true
			n.seen = map[string]bool{}
			return nil
		}
		n.run = execRun(n.cfg.Timeout)
	}
	raw, err := n.run(context.Background(), n.cfg.Command, "list", "counters")
	if err != nil {
		return fmt.Errorf("nftables: nft unavailable: %w", err)
	}
	if len(parseNftCounters(string(raw))) == 0 {
		return fmt.Errorf("nftables: no counters")
	}
	n.seen = map[string]bool{}
	return nil
}

func (n *nftablesCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	if !n.netlink && !sampleDue(&n.last, now, nftablesEvery) {
		return nil
	}
	var counters []nftCounter
	if n.netlink {
		if got, ok := readNftCountersNetlink(); ok {
			counters = got
		}
	}
	if counters == nil {
		raw, err := n.run(ctx, n.cfg.Command, "list", "counters")
		if err != nil {
			return err
		}
		counters = parseNftCounters(string(raw))
	}
	for _, c := range counters {
		id := sanitizeID(c.Table + "_" + c.Name)
		pkts := "netfilter.nftables_packets." + id
		bytes := "netfilter.nftables_bytes." + id
		if !n.seen[pkts] {
			n.seen[pkts] = true
			ch := sysChart(pkts, "nftables", "nftables packets "+c.Name, "packets/s", 8065, incDim("packets"))
			ch.Plugin, ch.Module = "nftables", "nftables"
			reg.AddChart(ch)
			b := sysChart(bytes, "nftables", "nftables bytes "+c.Name, "bytes/s", 8066, incDim("bytes"))
			b.Plugin, b.Module = "nftables", "nftables"
			reg.AddChart(b)
		}
		_ = reg.Collect(pkts, now, map[string]float64{"packets": c.Packets})
		_ = reg.Collect(bytes, now, map[string]float64{"bytes": c.Bytes})
	}
	return nil
}

type nftCounter struct {
	Table, Name    string
	Packets, Bytes float64
}

func parseNftCounters(s string) []nftCounter {
	var out []nftCounter
	table := "filter"
	var cur *nftCounter
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "table ") {
			f := strings.Fields(line)
			if len(f) >= 3 {
				table = f[2]
			}
			continue
		}
		if strings.HasPrefix(line, "counter ") {
			name := strings.Fields(line)[1]
			name = strings.Trim(name, "{")
			c := nftCounter{Table: table, Name: name}
			out = append(out, c)
			cur = &out[len(out)-1]
			continue
		}
		if cur == nil {
			continue
		}
		if strings.Contains(line, "packets") && strings.Contains(line, "bytes") {
			f := strings.Fields(line)
			for i := 0; i+1 < len(f); i++ {
				if f[i] == "packets" {
					cur.Packets = firstFloat(f[i+1])
				}
				if f[i] == "bytes" {
					cur.Bytes = firstFloat(f[i+1])
				}
			}
			cur = nil
		}
	}
	return out
}
