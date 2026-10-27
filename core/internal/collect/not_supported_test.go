package collect

import (
	"context"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/preprocess"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestDependentNotSupportedSetsChartError(t *testing.T) {
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	reg.AddChart(&registry.Chart{ID: "system.ram", Dimensions: []*registry.Dimension{{ID: "used"}}})
	dep := &dependentCollector{cfg: dependentConfig{Items: []dependentItem{{
		Name: "used", Chart: "system.ram", Dimension: "used",
		Steps: []preprocess.Step{{Type: "not_supported", Pattern: `^0$`}},
	}}}}
	if err := dep.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := reg.Collect("system.ram", time.Unix(100, 0), map[string]float64{"used": 0}); err != nil {
		t.Fatal(err)
	}
	if err := dep.Collect(context.Background(), reg, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("dependent.used")
	if !ok || ch.Unsupported == "" {
		t.Fatalf("unsupported %q", ch.Unsupported)
	}
	if ts := ch.LastCollectionTime(); ts != 0 {
		t.Fatalf("wrote a sample at %d", ts)
	}
	if err := reg.Collect("system.ram", time.Unix(110, 0), map[string]float64{"used": 4}); err != nil {
		t.Fatal(err)
	}
	if err := dep.Collect(context.Background(), reg, time.Unix(110, 0)); err != nil {
		t.Fatal(err)
	}
	if ch.Unsupported != "" {
		t.Fatalf("error stuck: %q", ch.Unsupported)
	}
	if ts, vals := ch.LastCollectionTime(), func() float64 { _, v := ch.LastValues(); return v["value"] }(); ts != 110 || vals != 4 {
		t.Fatalf("recovered at=%d value=%v", ts, vals)
	}
}

func TestDependentFollowsMasterUnsupported(t *testing.T) {
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	reg.AddChart(&registry.Chart{ID: "system.ram", Dimensions: []*registry.Dimension{{ID: "used"}}})
	if err := reg.Collect("system.ram", time.Unix(100, 0), map[string]float64{"used": 5}); err != nil {
		t.Fatal(err)
	}
	master, _ := reg.Chart("system.ram")
	master.SetUnsupported("snmp: down")
	dep := &dependentCollector{cfg: dependentConfig{Items: []dependentItem{{
		Name: "used_x2", Chart: "system.ram", Dimension: "used",
		Steps: []preprocess.Step{{Type: "multiplier", Factor: 2}},
	}}}}
	if err := dep.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := dep.Collect(context.Background(), reg, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	ch := mustChart(t, reg, "dependent.used_x2")
	if ch.UnsupportedMessage() == "" || ch.LastCollectionTime() != 0 {
		t.Fatalf("master unsupported did not propagate: %q at %d", ch.UnsupportedMessage(), ch.LastCollectionTime())
	}
	master.ClearUnsupported()
	if err := dep.Collect(context.Background(), reg, time.Unix(110, 0)); err != nil {
		t.Fatal(err)
	}
	if msg := ch.UnsupportedMessage(); msg != "" {
		t.Fatalf("error stuck: %q", msg)
	}
	if ts, vals := ch.LastCollectionTime(), func() float64 { _, v := ch.LastValues(); return v["value"] }(); ts != 110 || vals != 10 {
		t.Fatalf("recovered at=%d value=%v", ts, vals)
	}
}
