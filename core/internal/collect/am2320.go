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

// am2320Config is collectors.modules.am2320 (python.d AM2320 I2C via sysfs).
type am2320Config struct {
	Path string `yaml:"path"` // directory with in_temp_input / in_humidityrelative_input or temp/humidity
}

type am2320Collector struct {
	cfg      am2320Config
	readFile func(path string) ([]byte, error)
	readDir  func(dir string) ([]string, error)
	dir      string
}

func init() {
	Register("am2320", func() Collector { return &am2320Collector{} })
}

func (a *am2320Collector) Name() string { return "am2320" }

func (a *am2320Collector) Configure(decode func(v any) error) error {
	return decode(&a.cfg)
}

func (a *am2320Collector) Init(reg *registry.Registry) error {
	if a.readFile == nil {
		a.readFile = os.ReadFile
	}
	if a.readDir == nil {
		a.readDir = func(dir string) ([]string, error) {
			ents, err := os.ReadDir(dir)
			if err != nil {
				return nil, err
			}
			out := make([]string, 0, len(ents))
			for _, e := range ents {
				out = append(out, e.Name())
			}
			return out, nil
		}
	}
	dir, err := a.findDir()
	if err != nil {
		return err
	}
	a.dir = dir
	if _, _, err := a.read(); err != nil {
		return err
	}
	for _, ch := range []*registry.Chart{
		{ID: "am2320.temperature", Context: "am2320.temperature", Title: "AM2320 temperature", Units: "celsius", Family: "sensors", Priority: 64700,
			Dimensions: []*registry.Dimension{{ID: "temperature"}}},
		{ID: "am2320.humidity", Context: "am2320.humidity", Title: "AM2320 relative humidity", Units: "percentage", Family: "sensors", Type: registry.Area, Priority: 64710,
			Dimensions: []*registry.Dimension{{ID: "humidity"}}},
	} {
		ch.Plugin, ch.Module = "python.d", "am2320"
		reg.AddChart(ch)
	}
	return nil
}

func (a *am2320Collector) Collect(_ context.Context, reg *registry.Registry, now time.Time) error {
	temp, hum, err := a.read()
	if err != nil {
		return err
	}
	_ = reg.Collect("am2320.temperature", now, map[string]float64{"temperature": temp})
	_ = reg.Collect("am2320.humidity", now, map[string]float64{"humidity": hum})
	return nil
}

func (a *am2320Collector) findDir() (string, error) {
	if a.cfg.Path != "" {
		return a.cfg.Path, nil
	}
	roots := []string{"/sys/bus/iio/devices", "/sys/bus/i2c/devices"}
	for _, root := range roots {
		names, err := a.readDir(root)
		if err != nil {
			continue
		}
		for _, n := range names {
			nl := strings.ToLower(n)
			if strings.Contains(nl, "am2320") || strings.Contains(nl, "005c") || strings.HasPrefix(nl, "iio:device") {
				p := filepath.Join(root, n)
				if _, _, err := a.readFrom(p); err == nil {
					return p, nil
				}
			}
		}
	}
	return "", fmt.Errorf("am2320: sensor not found")
}

func (a *am2320Collector) read() (temp, hum float64, err error) {
	return a.readFrom(a.dir)
}

func (a *am2320Collector) readFrom(dir string) (temp, hum float64, err error) {
	temp, err = a.readScaled(dir, "in_temp_input", "temp", "temperature")
	if err != nil {
		return 0, 0, fmt.Errorf("am2320: %w", err)
	}
	hum, err = a.readScaled(dir, "in_humidityrelative_input", "humidity", "rh")
	if err != nil {
		return 0, 0, fmt.Errorf("am2320: %w", err)
	}
	return temp, hum, nil
}

func (a *am2320Collector) readScaled(dir string, names ...string) (float64, error) {
	var last error
	for _, n := range names {
		b, err := a.readFile(filepath.Join(dir, n))
		if err != nil {
			last = err
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
		if err != nil {
			last = err
			continue
		}
		if v > 200 { // IIO millidegrees / milli-percent
			v /= 1000
		}
		return v, nil
	}
	if last == nil {
		last = fmt.Errorf("missing %s", names[0])
	}
	return 0, last
}
