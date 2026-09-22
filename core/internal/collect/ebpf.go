package collect

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// ebpfConfig is collectors.modules.ebpf (`bpftool prog show`). Portable
// approximation of Netdata's ebpf.plugin: program count / memlock / run time.
type ebpfConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type ebpfCollector struct {
	cfg ebpfConfig
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func init() {
	Register("ebpf", func() Collector { return &ebpfCollector{} })
}

func (e *ebpfCollector) Name() string { return "ebpf" }

func (e *ebpfCollector) Configure(decode func(v any) error) error {
	if err := decode(&e.cfg); err != nil {
		return err
	}
	if e.cfg.Command == "" {
		e.cfg.Command = "bpftool"
	}
	if e.cfg.Timeout <= 0 {
		e.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (e *ebpfCollector) Init(reg *registry.Registry) error {
	if e.cfg.Command == "" {
		if err := e.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if e.run == nil {
		e.run = execRun(e.cfg.Timeout)
	}
	if _, err := e.programs(context.Background()); err != nil {
		if _, statErr := os.Stat("/sys/fs/bpf"); statErr != nil {
			return fmt.Errorf("ebpf: bpftool unavailable and /sys/fs/bpf missing")
		}
		return err
	}
	for _, c := range []*registry.Chart{
		{ID: "ebpf.programs", Title: "eBPF programs loaded", Units: "programs", Priority: 58200,
			Dimensions: []*registry.Dimension{{ID: "loaded"}}},
		{ID: "ebpf.bytes", Title: "eBPF program memlock", Units: "bytes", Priority: 58210, Type: registry.Area,
			Dimensions: []*registry.Dimension{{ID: "memlock"}}},
		{ID: "ebpf.run_time", Title: "eBPF program run time", Units: "ns/s", Priority: 58220, Type: registry.Area,
			Dimensions: []*registry.Dimension{incDim("run_time")}},
		{ID: "ebpf.run_count", Title: "eBPF program invocations", Units: "calls/s", Priority: 58230,
			Dimensions: []*registry.Dimension{incDim("run_cnt")}},
	} {
		c.Family, c.Plugin, c.Module = "ebpf", "ebpf", "ebpf"
		reg.AddChart(c)
	}
	return nil
}

func (e *ebpfCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	progs, err := e.programs(ctx)
	if err != nil {
		return err
	}
	var mem, runNS, runCnt float64
	for _, p := range progs {
		mem += p.Memlock
		runNS += p.RunTimeNS
		runCnt += p.RunCnt
	}
	_ = reg.Collect("ebpf.programs", now, map[string]float64{"loaded": float64(len(progs))})
	_ = reg.Collect("ebpf.bytes", now, map[string]float64{"memlock": mem})
	_ = reg.Collect("ebpf.run_time", now, map[string]float64{"run_time": runNS})
	_ = reg.Collect("ebpf.run_count", now, map[string]float64{"run_cnt": runCnt})
	return nil
}

func (e *ebpfCollector) Functions() []Function {
	return []Function{{
		Name:    "ebpf-programs",
		Help:    "Loaded eBPF programs (bpftool prog show)",
		Timeout: 8,
		Run: func(ctx context.Context, _ map[string]string) (any, error) {
			progs, err := e.programs(ctx)
			if err != nil {
				return nil, err
			}
			tab := Table{Columns: []string{"id", "type", "name", "memlock", "run_cnt", "run_time_ns"}, Total: len(progs), Rows: make([]any, len(progs))}
			for i, p := range progs {
				tab.Rows[i] = p
			}
			return tab, nil
		},
	}}
}

func (e *ebpfCollector) programs(ctx context.Context) ([]ebpfProg, error) {
	out, err := e.run(ctx, e.cfg.Command, "prog", "show")
	if err != nil {
		return nil, err
	}
	progs := parseBpftoolProg(string(out))
	if len(progs) == 0 && strings.TrimSpace(string(out)) != "" && !strings.Contains(string(out), ":") {
		return nil, fmt.Errorf("ebpf: unrecognised bpftool output")
	}
	return progs, nil
}

type ebpfProg struct {
	ID        int     `json:"id"`
	Type      string  `json:"type"`
	Name      string  `json:"name"`
	Memlock   float64 `json:"memlock"`
	RunCnt    float64 `json:"run_cnt"`
	RunTimeNS float64 `json:"run_time_ns"`
}

func parseBpftoolProg(s string) []ebpfProg {
	var out []ebpfProg
	var cur *ebpfProg
	sc := bufio.NewScanner(strings.NewReader(s))
	flush := func() {
		if cur != nil && cur.ID != 0 {
			out = append(out, *cur)
		}
		cur = nil
	}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		// "1: kprobe  name sys_open  tag abc  gpl"
		if i := strings.IndexByte(line, ':'); i > 0 && i < 8 {
			id, err := strconv.Atoi(line[:i])
			if err == nil {
				flush()
				p := ebpfProg{ID: id}
				rest := strings.Fields(line[i+1:])
				if len(rest) > 0 {
					p.Type = rest[0]
				}
				for j, tok := range rest {
					if tok == "name" && j+1 < len(rest) {
						p.Name = rest[j+1]
					}
				}
				cur = &p
				continue
			}
		}
		if cur == nil {
			continue
		}
		if v := fieldAfter(line, "memlock"); v != "" {
			cur.Memlock = parseByteSize(v)
		}
		if v := fieldAfter(line, "run_time_ns"); v != "" {
			cur.RunTimeNS, _ = strconv.ParseFloat(strings.TrimSuffix(v, "ns"), 64)
		}
		if v := fieldAfter(line, "run_cnt"); v != "" {
			cur.RunCnt, _ = strconv.ParseFloat(v, 64)
		}
	}
	flush()
	return out
}

func fieldAfter(line, key string) string {
	f := strings.Fields(line)
	for i, tok := range f {
		if tok == key && i+1 < len(f) {
			return f[i+1]
		}
	}
	return ""
}

func parseByteSize(s string) float64 {
	s = strings.TrimSpace(s)
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "KiB"), strings.HasSuffix(s, "K"):
		mult = 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "KiB"), "K")
	case strings.HasSuffix(s, "MiB"), strings.HasSuffix(s, "M"):
		mult = 1024 * 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "MiB"), "M")
	case strings.HasSuffix(s, "B"):
		s = strings.TrimSuffix(s, "B")
	}
	n, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return n * mult
}
