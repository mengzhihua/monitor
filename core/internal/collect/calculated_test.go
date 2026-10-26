package collect

import (
	"context"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
	"gopkg.in/yaml.v3"
)

func TestCalculatedItemCombinesDimensions(t *testing.T) {
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	reg.AddChart(&registry.Chart{ID: "system.ram", Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "free"}}})
	if err := reg.Collect("system.ram", time.Unix(100, 0), map[string]float64{"used": 25, "free": 75}); err != nil {
		t.Fatal(err)
	}
	var node yaml.Node
	raw := "items:\n  - name: used_pct\n    formula: \"100 * {system.ram:used} / ({system.ram:used} + {system.ram:free})\"\n"
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	c := &calculatedCollector{}
	if err := c.Configure(node.Decode); err != nil {
		t.Fatal(err)
	}
	if err := c.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Collect(context.Background(), reg, time.Unix(101, 0)); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("calculated.used_pct")
	if !ok {
		t.Fatal("calculated chart missing")
	}
	_, vals := ch.LastValues()
	if vals["value"] != 25 {
		t.Fatalf("value=%v", vals["value"])
	}
}

func TestCalculatedRejectsUnsafeFormulas(t *testing.T) {
	c := &calculatedCollector{}
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("items:\n  - {name: bad, formula: \"{system.ram:used} + system(\\\"rm\\\")\"}\n"), &node); err != nil {
		t.Fatal(err)
	}
	if err := c.Configure(node.Decode); err == nil {
		t.Fatal("accepted a formula that is not arithmetic")
	}
	if _, err := evalFormula("1 / 0", func(string, string) (float64, bool) { return 0, false }, nil); err == nil {
		t.Fatal("division by zero")
	}
	for _, formula := range []string{
		"avg({system.ram:used}, 2h)",
		"sum({system.ram:used}, 0s)",
		"foo({system.ram:used}, 5m)",
		"avg(1, 5m)",
	} {
		if err := yaml.Unmarshal([]byte("items:\n  - {name: bad, formula: "+strconv.Quote(formula)+"}\n"), &node); err != nil {
			t.Fatal(err)
		}
		if err := c.Configure(node.Decode); err == nil {
			t.Fatalf("accepted %s", formula)
		}
	}
}

func TestCalculatedWindowUsesStoredSamples(t *testing.T) {
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, db)
	reg.AddChart(&registry.Chart{ID: "system.ram", Dimensions: []*registry.Dimension{{ID: "used"}}})
	for _, sample := range []struct {
		at int64
		v  float64
	}{{94, 1000}, {95, 10}, {105, 30}, {110, 50}} {
		if err := reg.Collect("system.ram", time.Unix(sample.at, 0), map[string]float64{"used": sample.v}); err != nil {
			t.Fatal(err)
		}
	}
	var node yaml.Node
	raw := "" +
		"items:\n" +
		"  - {name: used_avg, formula: \"avg({system.ram:used}, 15s)\"}\n" +
		"  - {name: used_min, formula: \"min({system.ram:used}, 15s)\"}\n" +
		"  - {name: used_max, formula: \"max({system.ram:used}, 15s)\"}\n" +
		"  - {name: used_sum, formula: \"sum({system.ram:used}, 15s)\"}\n" +
		"  - {name: used_count, formula: \"count({system.ram:used}, 15s)\"}\n" +
		"  - {name: used_last, formula: \"last({system.ram:used}, 15s)\"}\n" +
		"  - {name: mixed, formula: \"avg({system.ram:used}, 15s) + {system.ram:used}\"}\n"
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	sched := NewScheduler(reg, nil, Options{
		Names:   []string{"calculated"},
		Modules: map[string]func(any) error{"calculated": node.Decode},
		History: db,
		Timeout: time.Second,
	})
	t.Cleanup(func() { sched.Close() })
	if sched.Collector("calculated") == nil {
		t.Fatal("calculated collector missing")
	}
	for _, st := range sched.Status() {
		if st.Name == "calculated" && st.Error != "" {
			t.Fatal(st.Error)
		}
	}
	if err := sched.Collector("calculated").Collect(context.Background(), reg, time.Unix(110, 0)); err != nil {
		t.Fatal(err)
	}
	// 15s ending at 110 keeps 95, 105 and 110 (10, 30, 50). The sample at 94 stays outside.
	want := map[string]float64{"calculated.used_avg": 30, "calculated.used_min": 10, "calculated.used_max": 50, "calculated.used_sum": 90, "calculated.used_count": 3, "calculated.used_last": 50, "calculated.mixed": 80}
	for id, value := range want {
		ch, ok := reg.Chart(id)
		if !ok {
			t.Fatalf("missing %s", id)
		}
		_, vals := ch.LastValues()
		if vals["value"] != value {
			t.Fatalf("%s=%v", id, vals["value"])
		}
	}
}

func TestCalculatedCountAndLast(t *testing.T) {
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, db)
	reg.AddChart(&registry.Chart{ID: "system.ram", Dimensions: []*registry.Dimension{{ID: "used"}}})
	// Newest sample is 4, not the larger older value 9. A NaN must not count.
	for _, sample := range []struct {
		at int64
		v  float64
	}{{100, 9}, {104, 1}, {106, math.NaN()}, {108, 4}} {
		if err := reg.Collect("system.ram", time.Unix(sample.at, 0), map[string]float64{"used": sample.v}); err != nil {
			t.Fatal(err)
		}
	}
	var node yaml.Node
	raw := "" +
		"items:\n" +
		"  - {name: n, formula: \"count({system.ram:used}, 10s)\"}\n" +
		"  - {name: latest, formula: \"last({system.ram:used}, 10s)\"}\n" +
		"  - {name: none, formula: \"count({system.ram:free}, 10s)\"}\n"
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	sched := NewScheduler(reg, nil, Options{
		Names:   []string{"calculated"},
		Modules: map[string]func(any) error{"calculated": node.Decode},
		History: db,
		Timeout: time.Second,
	})
	t.Cleanup(func() { sched.Close() })
	if err := sched.Collector("calculated").Collect(context.Background(), reg, time.Unix(108, 0)); err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{"calculated.n": 3, "calculated.latest": 4, "calculated.none": 0}
	for id, value := range want {
		ch, ok := reg.Chart(id)
		if !ok {
			t.Fatalf("missing %s", id)
		}
		_, vals := ch.LastValues()
		if vals["value"] != value {
			t.Fatalf("%s=%v", id, vals["value"])
		}
	}
}

func TestCalculatedWindowWithoutHistorySkips(t *testing.T) {
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	reg.AddChart(&registry.Chart{ID: "system.ram", Dimensions: []*registry.Dimension{{ID: "used"}}})
	if err := reg.Collect("system.ram", time.Unix(100, 0), map[string]float64{"used": 10}); err != nil {
		t.Fatal(err)
	}
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("items:\n  - {name: used_avg, formula: \"avg({system.ram:used}, 5m)\"}\n"), &node); err != nil {
		t.Fatal(err)
	}
	c := &calculatedCollector{}
	if err := c.Configure(node.Decode); err != nil {
		t.Fatal(err)
	}
	if err := c.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Collect(context.Background(), reg, time.Unix(110, 0)); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("calculated.used_avg")
	if !ok {
		t.Fatal("chart missing")
	}
	_, vals := ch.LastValues()
	if _, ok := vals["value"]; ok {
		t.Fatalf("wrote %v without stored samples", vals["value"])
	}
}
