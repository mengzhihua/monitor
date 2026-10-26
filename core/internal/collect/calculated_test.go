package collect

import (
	"context"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
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
	if _, err := evalFormula("1 / 0", func(string, string) (float64, bool) { return 0, false }); err == nil {
		t.Fatal("division by zero")
	}
}
