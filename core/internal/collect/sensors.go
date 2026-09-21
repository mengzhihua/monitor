package collect

import (
	"context"
	"errors"
	"time"

	"github.com/shirou/gopsutil/v4/sensors"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

type sensorsCollector struct {
	known map[string]bool
}

func init() {
	Register("sensors", func() Collector { return &sensorsCollector{} })
}

func (s *sensorsCollector) Name() string { return "sensors" }

func (s *sensorsCollector) Init(reg *registry.Registry) error {
	temps, err := sensors.SensorsTemperatures()
	if err != nil {
		return err
	}
	if len(temps) == 0 {
		return errors.New("no temperature sensors")
	}
	s.known = map[string]bool{}
	ch := &registry.Chart{ID: "sensors.temperature", Family: "sensors", Title: "Temperature sensors", Units: "Celsius",
		Priority: 5000, Plugin: "system", Module: "sensors"}
	for _, t := range temps {
		id := sensorID(t.SensorKey)
		if s.known[id] {
			continue
		}
		s.known[id] = true
		ch.Dimensions = append(ch.Dimensions, &registry.Dimension{ID: id, Name: t.SensorKey})
	}
	if len(ch.Dimensions) == 0 {
		return errors.New("no temperature sensors")
	}
	reg.AddChart(ch)
	return nil
}

func (s *sensorsCollector) Collect(_ context.Context, reg *registry.Registry, now time.Time) error {
	temps, err := sensors.SensorsTemperatures()
	if err != nil {
		return err
	}
	ch, ok := reg.Chart("sensors.temperature")
	if !ok {
		return nil
	}
	vals := map[string]float64{}
	for _, t := range temps {
		id := sensorID(t.SensorKey)
		if !s.known[id] {
			s.known[id] = true
			ch.AddDimension(&registry.Dimension{ID: id, Name: t.SensorKey})
		}
		vals[id] = t.Temperature
	}
	return reg.Collect("sensors.temperature", now, vals)
}

func sensorID(key string) string {
	id := sanitizeID(key)
	if id == "" {
		return "sensor"
	}
	return id
}
