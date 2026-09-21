package collect

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// w1sensorConfig is collectors.modules.w1sensor (/sys/bus/w1/devices).
type w1sensorConfig struct {
	SensorsPath string `yaml:"sensors_path"`
}

type w1sensorCollector struct {
	cfg      w1sensorConfig
	readDir  func(dir string) ([]string, error)
	readFile func(path string) ([]byte, error)
	dir      string
	seen     map[string]bool
}

func init() {
	Register("w1sensor", func() Collector { return &w1sensorCollector{} })
}

func (w *w1sensorCollector) Name() string { return "w1sensor" }

func (w *w1sensorCollector) Configure(decode func(v any) error) error {
	return decode(&w.cfg)
}

func (w *w1sensorCollector) Init(reg *registry.Registry) error {
	if w.readDir == nil {
		w.readDir = func(dir string) ([]string, error) {
			ents, err := os.ReadDir(dir)
			if err != nil {
				return nil, err
			}
			out := make([]string, 0, len(ents))
			for _, e := range ents {
				if e.IsDir() {
					out = append(out, e.Name())
				}
			}
			return out, nil
		}
	}
	if w.readFile == nil {
		w.readFile = os.ReadFile
	}
	dir := w.cfg.SensorsPath
	if dir == "" {
		dir = "/sys/bus/w1/devices"
	}
	w.dir = dir
	w.seen = map[string]bool{}
	temps, err := w.temps()
	if err != nil {
		return err
	}
	if len(temps) == 0 {
		return fmt.Errorf("w1sensor: no w1 sensors found")
	}
	return nil
}

func (w *w1sensorCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	temps, err := w.temps()
	if err != nil {
		return err
	}
	if len(temps) == 0 {
		return fmt.Errorf("w1sensor: no w1 sensors found")
	}
	for id, t := range temps {
		if !w.seen[id] {
			w.seen[id] = true
			ch := &registry.Chart{
				ID: "w1sensor.temperature." + id, Context: "w1sensor.temperature",
				Title: "1-Wire Temperature Sensor", Units: "Celsius", Priority: 60700,
				Dimensions: []*registry.Dimension{{ID: "temperature"}},
			}
			ch.Family, ch.Plugin, ch.Module = "w1sensor", "w1sensor", "w1sensor"
			reg.AddChart(ch)
		}
		_ = reg.Collect("w1sensor.temperature."+id, now, map[string]float64{"temperature": t})
	}
	return nil
}

func (w *w1sensorCollector) temps() (map[string]float64, error) {
	names, err := w.readDir(w.dir)
	if err != nil {
		return nil, fmt.Errorf("w1sensor: %w", err)
	}
	out := map[string]float64{}
	for _, name := range names {
		if !isW1sensorDir(name) {
			continue
		}
		b, err := w.readFile(filepath.Join(w.dir, name, "w1_slave"))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("w1sensor: %w", err)
		}
		t, err := parseW1Temperature(string(b))
		if err != nil {
			return nil, fmt.Errorf("w1sensor: %s: %w", name, err)
		}
		out[sanitizeID(name)] = t
	}
	return out, nil
}

func parseW1Temperature(body string) (float64, error) {
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) < 2 {
		return 0, fmt.Errorf("no temperature found")
	}
	_, tempStr, ok := strings.Cut(strings.TrimSpace(lines[len(lines)-1]), "t=")
	if !ok {
		return 0, fmt.Errorf("no temperature found")
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(tempStr), 64)
	if err != nil {
		return 0, err
	}
	return v / 1000, nil
}

func isW1sensorDir(dirName string) bool {
	for _, px := range []string{"10-", "22-", "28-", "3b-", "42-"} {
		if strings.HasPrefix(dirName, px) {
			return true
		}
	}
	return false
}
