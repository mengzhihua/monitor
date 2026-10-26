package collect

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/mengzhihua/monitor/core/internal/preprocess"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

// dependent items read another chart dimension and run the preprocess pipeline.
type dependentConfig struct {
	Items []dependentItem `yaml:"items"`
}

type dependentItem struct {
	Name      string            `yaml:"name"`
	Chart     string            `yaml:"chart"`
	Dimension string            `yaml:"dimension"`
	Steps     []preprocess.Step `yaml:"preprocess"`
}

type dependentCollector struct {
	cfg     dependentConfig
	prev    map[string]float64
	emitted map[string]int64
}

func init() { Register("dependent", func() Collector { return &dependentCollector{} }) }

func (d *dependentCollector) Name() string { return "dependent" }

func (d *dependentCollector) Configure(decode func(v any) error) error {
	if err := decode(&d.cfg); err != nil {
		return err
	}
	if len(d.cfg.Items) > 64 {
		return fmt.Errorf("dependent: at most 64 items")
	}
	for _, it := range d.cfg.Items {
		if !validItemName(it.Name) || it.Chart == "" || it.Dimension == "" {
			return fmt.Errorf("dependent: item %q needs chart and dimension", it.Name)
		}
		if err := preprocess.Validate(it.Steps); err != nil {
			return fmt.Errorf("dependent: item %q: %w", it.Name, err)
		}
	}
	return nil
}

func (d *dependentCollector) Init(reg *registry.Registry) error {
	if len(d.cfg.Items) == 0 {
		return fmt.Errorf("dependent: no items")
	}
	d.prev = map[string]float64{}
	d.emitted = map[string]int64{}
	for _, it := range d.cfg.Items {
		reg.AddChart(&registry.Chart{ID: "dependent." + sanitizeID(it.Name), Context: "dependent.value", Title: "Dependent " + it.Name,
			Units: "value", Family: "calc", Plugin: "dependent", Module: "dependent", Priority: 56600,
			Labels:     map[string]string{"master_chart": it.Chart, "master_dimension": it.Dimension},
			Dimensions: []*registry.Dimension{{ID: "value"}}})
	}
	return nil
}

func (d *dependentCollector) Collect(_ context.Context, reg *registry.Registry, now time.Time) error {
	for _, it := range d.cfg.Items {
		ch, ok := reg.Chart(it.Chart)
		if !ok {
			continue
		}
		_, vals := ch.LastValues()
		raw, ok := vals[it.Dimension]
		if !ok || math.IsNaN(raw) || math.IsInf(raw, 0) {
			continue
		}
		prev := math.NaN()
		if v, ok := d.prev[it.Name]; ok {
			prev = v
		}
		value, next, emitted, err := preprocess.ApplyWithHeartbeat(it.Steps, strconv.FormatFloat(raw, 'f', -1, 64), prev, d.emitted[it.Name], now.Unix())
		if err != nil {
			continue
		}
		d.prev[it.Name] = next
		d.emitted[it.Name] = emitted
		_ = reg.Collect("dependent."+sanitizeID(it.Name), now, map[string]float64{"value": value})
	}
	return nil
}
