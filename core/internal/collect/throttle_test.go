package collect

import (
	"context"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"gopkg.in/yaml.v3"
)

func TestDependentThrottleKeepsOneValuePerWindow(t *testing.T) {
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	reg.AddChart(&registry.Chart{ID: "system.ram", Dimensions: []*registry.Dimension{{ID: "used"}}})
	var node yaml.Node
	raw := "items:\n  - name: used\n    chart: system.ram\n    dimension: used\n    preprocess:\n      - {type: throttle, seconds: 10}\n"
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	dep := &dependentCollector{}
	if err := dep.Configure(node.Decode); err != nil {
		t.Fatal(err)
	}
	if err := dep.Init(reg); err != nil {
		t.Fatal(err)
	}
	collectAt := func(sec int64, used float64) (int64, float64) {
		t.Helper()
		if err := reg.Collect("system.ram", time.Unix(sec, 0), map[string]float64{"used": used}); err != nil {
			t.Fatal(err)
		}
		if err := dep.Collect(context.Background(), reg, time.Unix(sec, 0)); err != nil {
			t.Fatal(err)
		}
		ch, ok := reg.Chart("dependent.used")
		if !ok {
			t.Fatal("dependent chart missing")
		}
		_, vals := ch.LastValues()
		return ch.LastCollectionTime(), vals["value"]
	}
	if at, value := collectAt(100, 4); at != 100 || value != 4 {
		t.Fatalf("first at=%d value=%v", at, value)
	}
	if at, value := collectAt(109, 9); at != 100 || value != 4 {
		t.Fatalf("changed value inside the window at=%d value=%v", at, value)
	}
	if at, value := collectAt(110, 9); at != 110 || value != 9 {
		t.Fatalf("next window at=%d value=%v", at, value)
	}
}

func TestDependentRejectsThrottlePeriod(t *testing.T) {
	var node yaml.Node
	raw := "items:\n  - {name: used, chart: system.ram, dimension: used, preprocess: [{type: throttle, seconds: 0}]}\n"
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	if err := (&dependentCollector{}).Configure(node.Decode); err == nil {
		t.Fatal("accepted a zero throttle")
	}
}
