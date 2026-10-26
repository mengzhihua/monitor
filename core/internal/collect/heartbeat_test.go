package collect

import (
	"context"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"gopkg.in/yaml.v3"
)

func TestDependentHeartbeatWritesAgain(t *testing.T) {
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	reg.AddChart(&registry.Chart{ID: "system.ram", Dimensions: []*registry.Dimension{{ID: "used"}}})
	var node yaml.Node
	raw := "items:\n  - name: used\n    chart: system.ram\n    dimension: used\n    preprocess:\n      - {type: discard_unchanged_heartbeat, seconds: 10}\n"
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
		t.Fatalf("first sample at=%d value=%v", at, value)
	}
	if at, value := collectAt(109, 4); at != 100 || value != 4 {
		t.Fatalf("repeat inside the window at=%d value=%v", at, value)
	}
	if at, value := collectAt(110, 4); at != 110 || value != 4 {
		t.Fatalf("heartbeat at=%d value=%v", at, value)
	}
	if at, value := collectAt(112, 9); at != 112 || value != 9 {
		t.Fatalf("change at=%d value=%v", at, value)
	}
	if at, _ := collectAt(121, 9); at != 112 {
		t.Fatalf("change restarted the window, wrote at %d", at)
	}
	if at, value := collectAt(122, 9); at != 122 || value != 9 {
		t.Fatalf("next heartbeat at=%d value=%v", at, value)
	}
}

func TestDependentRejectsHeartbeatPeriod(t *testing.T) {
	var node yaml.Node
	raw := "items:\n  - {name: used, chart: system.ram, dimension: used, preprocess: [{type: discard_unchanged_heartbeat, seconds: 0}]}\n"
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	if err := (&dependentCollector{}).Configure(node.Decode); err == nil {
		t.Fatal("accepted a zero heartbeat")
	}
}
