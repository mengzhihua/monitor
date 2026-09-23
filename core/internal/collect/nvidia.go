package collect

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// nvidiaConfig is collectors.modules.nvidia (nvidia-smi).
type nvidiaConfig struct {
	Command string `yaml:"command"` // default nvidia-smi
}

type nvidiaCollector struct {
	cfg  nvidiaConfig
	last time.Time
}

const nvidiaEvery = 5 * time.Second

func init() {
	Register("nvidia", func() Collector { return &nvidiaCollector{} })
}

func (n *nvidiaCollector) Name() string { return "nvidia" }

func (n *nvidiaCollector) Configure(decode func(v any) error) error {
	if err := decode(&n.cfg); err != nil {
		return err
	}
	if n.cfg.Command == "" {
		n.cfg.Command = "nvidia-smi"
	}
	return nil
}

func (n *nvidiaCollector) Init(reg *registry.Registry) error {
	if n.cfg.Command == "" {
		if err := n.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := n.query(context.Background()); err != nil {
		return err
	}
	for _, c := range []*registry.Chart{
		{ID: "nvidia_smi.gpu_utilization", Title: "NVIDIA GPU utilization", Units: "%", Priority: 50000},
		{ID: "nvidia_smi.mem_utilization", Title: "NVIDIA GPU memory utilization", Units: "%", Priority: 50010},
		{ID: "nvidia_smi.fb_memory", Title: "NVIDIA framebuffer memory", Units: "MiB", Type: registry.Stacked, Priority: 50020},
		{ID: "nvidia_smi.temperature", Title: "NVIDIA GPU temperature", Units: "celsius", Priority: 50030},
		{ID: "nvidia_smi.power", Title: "NVIDIA GPU power", Units: "watts", Priority: 50040},
	} {
		c.Family, c.Plugin, c.Module = "nvidia", "nvidia", "nvidia"
		reg.AddChart(c)
	}
	return nil
}

type nvGPU struct {
	id, name                         string
	util, memUtil, memUsed, memTotal float64
	temp, power                      float64
}

func (n *nvidiaCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	if !sampleDue(&n.last, now, nvidiaEvery) {
		return nil
	}
	gpus, err := n.query(ctx)
	if err != nil {
		return err
	}
	util, memu, used, total, temp, power := map[string]float64{}, map[string]float64{}, map[string]float64{}, map[string]float64{}, map[string]float64{}, map[string]float64{}
	ensure := func(id, chart string) {
		if ch, ok := reg.Chart(chart); ok && ch.Dimension(id) == nil {
			ch.AddDimension(&registry.Dimension{ID: id})
		}
	}
	for _, g := range gpus {
		id := g.id
		if g.name != "" {
			id = g.id + "_" + sanitizeDim(g.name)
		}
		ensure(id, "nvidia_smi.gpu_utilization")
		ensure(id, "nvidia_smi.mem_utilization")
		ensure(id+"_used", "nvidia_smi.fb_memory")
		ensure(id+"_free", "nvidia_smi.fb_memory")
		ensure(id, "nvidia_smi.temperature")
		ensure(id, "nvidia_smi.power")
		util[id] = g.util
		memu[id] = g.memUtil
		used[id+"_used"] = g.memUsed
		total[id+"_free"] = g.memTotal - g.memUsed
		temp[id] = g.temp
		power[id] = g.power
	}
	_ = reg.Collect("nvidia_smi.gpu_utilization", now, util)
	_ = reg.Collect("nvidia_smi.mem_utilization", now, memu)
	fb := map[string]float64{}
	for k, v := range used {
		fb[k] = v
	}
	for k, v := range total {
		fb[k] = v
	}
	_ = reg.Collect("nvidia_smi.fb_memory", now, fb)
	_ = reg.Collect("nvidia_smi.temperature", now, temp)
	_ = reg.Collect("nvidia_smi.power", now, power)
	return nil
}

func (n *nvidiaCollector) query(ctx context.Context) ([]nvGPU, error) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, n.cfg.Command,
		"--query-gpu=index,name,utilization.gpu,utilization.memory,memory.used,memory.total,temperature.gpu,power.draw",
		"--format=csv,noheader,nounits")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi: %w", err)
	}
	return parseNvidiaSMI(string(out))
}

func parseNvidiaSMI(s string) ([]nvGPU, error) {
	var out []nvGPU
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := splitCSV(line)
		if len(f) < 8 {
			continue
		}
		num := func(i int) float64 {
			v, _ := strconv.ParseFloat(strings.TrimSpace(f[i]), 64)
			return v
		}
		out = append(out, nvGPU{
			id: strings.TrimSpace(f[0]), name: strings.TrimSpace(f[1]),
			util: num(2), memUtil: num(3), memUsed: num(4), memTotal: num(5),
			temp: num(6), power: num(7),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("nvidia-smi: no gpus")
	}
	return out, nil
}

func splitCSV(s string) []string {
	var f []string
	for _, p := range strings.Split(s, ",") {
		f = append(f, strings.TrimSpace(p))
	}
	return f
}

func sanitizeDim(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "_")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		return "gpu"
	}
	return out
}
