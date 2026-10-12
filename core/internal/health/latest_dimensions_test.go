package health

import (
	"math"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestCalculatedAlarmsAndVariablesExcludeMissingLatestDimensions(t *testing.T) {
	rules := `
alarms:
  - name: current_used
    on: system.ram
    calc: $used
    every: 1s
    warn: '$this > 80'
  - name: current_alias
    on: system.ram
    calc: $usage_alias
    every: 1s
    warn: '$this > 80'
  - name: sample_age
    on: system.ram
    calc: '$now - $last_collected_t'
    every: 1s
    warn: '$this > 10'
  - name: recent_used
    on: system.ram
    lookup: average -1m unaligned of used
    every: 1s
    warn: '$this > 80'
`
	e, reg := newTestEngine(t, rules)
	reg.ReplaceChart(&registry.Chart{ID: "system.ram", Family: "ram", Units: "MiB", Dimensions: []*registry.Dimension{{ID: "used", Name: "usage_alias"}, {ID: "free"}}})
	now := time.Unix(1_700_000_000, 0)
	collect := func(at time.Time, values map[string]float64) {
		t.Helper()
		if err := reg.Collect("system.ram", at, values); err != nil {
			t.Fatal(err)
		}
		e.Tick(at.Add(time.Second))
	}
	collect(now, map[string]float64{"used": 90, "free": 10})
	for _, alarm := range e.Alarms() {
		if alarm.Name != "sample_age" && alarm.Status != StatusWarning {
			t.Fatalf("initial %s=%s value=%v", alarm.Name, alarm.Status, alarm.Value)
		}
	}
	collect(now.Add(2*time.Second), map[string]float64{"free": 30})
	for _, alarm := range e.Alarms() {
		switch alarm.Name {
		case "current_used", "current_alias":
			if alarm.Status != StatusUndefined || !math.IsNaN(alarm.Value) {
				t.Fatalf("missing latest dimension reused by %s: %s value=%v", alarm.Name, alarm.Status, alarm.Value)
			}
		case "sample_age":
			if alarm.Value != 1 || alarm.Status != StatusClear {
				t.Fatalf("latest collection timestamp lost: %+v", alarm)
			}
		case "recent_used":
			if alarm.Value != 90 || alarm.Status != StatusWarning {
				t.Fatalf("explicit historical lookup changed: %+v", alarm)
			}
		}
	}
	vars := e.ChartVariables("system.ram", "current_used")
	for _, name := range []string{"used", "usage_alias", "current_used", "current_alias"} {
		if _, present := vars[name]; present {
			t.Fatal("stale dimension exposed in available variables", name)
		}
	}
	if vars["free"] != float64(30) {
		t.Fatal("fresh dimension missing", vars)
	}
	collect(now.Add(4*time.Second), map[string]float64{"used": 20, "free": 80})
	for _, alarm := range e.Alarms() {
		if (alarm.Name == "current_used" || alarm.Name == "current_alias") && (alarm.Value != 20 || alarm.Status != StatusClear) {
			t.Fatalf("new sample did not restore calculation: %+v", alarm)
		}
	}
}
