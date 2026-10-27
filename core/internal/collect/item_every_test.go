package collect

import (
	"context"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"gopkg.in/yaml.v3"
)

func TestItemIntervalSkipsEarlySamples(t *testing.T) {
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	reg.AddChart(&registry.Chart{ID: "system.ram", Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "free"}}})
	var node yaml.Node
	raw := "items:\n  - {name: used, chart: system.ram, dimension: used, every: 10s}\n"
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
	calcNode := yaml.Node{}
	calcRaw := "items:\n  - {name: used, formula: \"{system.ram:used}\", every: 10s}\n"
	if err := yaml.Unmarshal([]byte(calcRaw), &calcNode); err != nil {
		t.Fatal(err)
	}
	calc := &calculatedCollector{}
	if err := calc.Configure(calcNode.Decode); err != nil {
		t.Fatal(err)
	}
	if err := calc.Init(reg); err != nil {
		t.Fatal(err)
	}
	sample := func(sec int64, used float64) {
		t.Helper()
		if err := reg.Collect("system.ram", time.Unix(sec, 0), map[string]float64{"used": used, "free": 1}); err != nil {
			t.Fatal(err)
		}
		if err := dep.Collect(context.Background(), reg, time.Unix(sec, 0)); err != nil {
			t.Fatal(err)
		}
		if err := calc.Collect(context.Background(), reg, time.Unix(sec, 0)); err != nil {
			t.Fatal(err)
		}
	}
	at := func(id string) (int64, float64) {
		t.Helper()
		ch, ok := reg.Chart(id)
		if !ok {
			t.Fatalf("missing %s", id)
		}
		_, vals := ch.LastValues()
		return ch.LastCollectionTime(), vals["value"]
	}
	sample(100, 4)
	if ts, value := at("dependent.used"); ts != 100 || value != 4 {
		t.Fatalf("dependent first %d %v", ts, value)
	}
	if ts, value := at("calculated.used"); ts != 100 || value != 4 {
		t.Fatalf("calculated first %d %v", ts, value)
	}
	sample(105, 9)
	if ts, value := at("dependent.used"); ts != 100 || value != 4 {
		t.Fatalf("dependent inside window %d %v", ts, value)
	}
	if ts, value := at("calculated.used"); ts != 100 || value != 4 {
		t.Fatalf("calculated inside window %d %v", ts, value)
	}
	sample(110, 9)
	if ts, value := at("dependent.used"); ts != 110 || value != 9 {
		t.Fatalf("dependent next %d %v", ts, value)
	}
	if ts, value := at("calculated.used"); ts != 110 || value != 9 {
		t.Fatalf("calculated next %d %v", ts, value)
	}
}

func TestItemIntervalRejectsRange(t *testing.T) {
	if err := itemEvery("item", 500*time.Millisecond); err == nil {
		t.Fatal("accepted a sub-second interval")
	}
	if err := itemEvery("item", 25*time.Hour); err == nil {
		t.Fatal("accepted an interval over a day")
	}
	if err := itemEvery("item", 0); err != nil {
		t.Fatal(err)
	}
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("items:\n  - {name: used, chart: system.ram, dimension: used, every: 500ms}\n"), &node); err != nil {
		t.Fatal(err)
	}
	if err := (&dependentCollector{}).Configure(node.Decode); err == nil {
		t.Fatal("accepted a 500ms dependent item")
	}
}
