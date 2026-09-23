package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// androidUsageConfig is collectors.modules.android_usage. The Android app
// writes this JSON from UsageStatsManager; other hosts leave the path empty
// and the collector disables itself.
type androidUsageConfig struct {
	Path string `yaml:"path"`
}

type androidAppSample struct {
	Name string  `json:"name"`
	CPU  float64 `json:"cpu_ms"`
	Rx   float64 `json:"rx"`
	Tx   float64 `json:"tx"`
}

type androidUsageFile struct {
	Apps []androidAppSample `json:"apps"`
}

type androidUsageCollector struct {
	cfg  androidUsageConfig
	seen map[string]bool
}

func init() {
	Register("android_usage", func() Collector { return &androidUsageCollector{} })
}

func (a *androidUsageCollector) Name() string { return "android_usage" }

func (a *androidUsageCollector) Configure(decode func(v any) error) error {
	return decode(&a.cfg)
}

func (a *androidUsageCollector) Init(reg *registry.Registry) error {
	if a.cfg.Path == "" {
		return fmt.Errorf("android_usage: no path")
	}
	if _, err := readAndroidUsage(a.cfg.Path); err != nil {
		return err
	}
	a.seen = map[string]bool{}
	ch := sysChart("android.apps", "android", "Android apps with usage samples", "apps", 91000,
		&registry.Dimension{ID: "apps"})
	ch.Plugin, ch.Module = "android", "android_usage"
	reg.AddChart(ch)
	return nil
}

func (a *androidUsageCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	rows, err := readAndroidUsage(a.cfg.Path)
	if err != nil {
		return err
	}
	_ = reg.Collect("android.apps", now, map[string]float64{"apps": float64(len(rows))})
	for _, row := range rows {
		id := sanitizeID(row.Name)
		cpu := "android.app_cpu." + id
		if !a.seen[cpu] {
			a.seen[cpu] = true
			ch := sysChart(cpu, "android", "App CPU "+row.Name, "milliseconds", 91010, incDim("cpu"))
			ch.Plugin, ch.Module = "android", "android_usage"
			reg.AddChart(ch)
			net := sysChart("android.app_net."+id, "android", "App traffic "+row.Name, "bytes/s", 91020, incDim("rx"), incDim("tx"))
			net.Plugin, net.Module = "android", "android_usage"
			reg.AddChart(net)
		}
		_ = reg.Collect(cpu, now, map[string]float64{"cpu": row.CPU})
		_ = reg.Collect("android.app_net."+id, now, map[string]float64{"rx": row.Rx, "tx": row.Tx})
	}
	return nil
}

func readAndroidUsage(path string) ([]androidAppSample, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("android_usage: %w", err)
	}
	var doc androidUsageFile
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("android_usage: %w", err)
	}
	return doc.Apps, nil
}
