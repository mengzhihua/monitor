package preprocess

import (
	"math"
	"testing"
)

func TestApplyPipeline(t *testing.T) {
	v, _, err := Apply([]Step{
		{Type: "trim"},
		{Type: "regex", Pattern: `load=([0-9.]+)`},
		{Type: "multiplier", Factor: 100},
	}, "  load=0.5\n", math.NaN())
	if err != nil || v != 50 {
		t.Fatalf("v=%v err=%v", v, err)
	}
	v, _, err = Apply([]Step{{Type: "jsonpath", Path: "$.mem.used"}}, `{"mem":{"used":12}}`, math.NaN())
	if err != nil || v != 12 {
		t.Fatalf("json %v %v", v, err)
	}
	v, _, err = Apply([]Step{{Type: "jsonpath", Path: "$.rows[1].n"}}, `{"rows":[{"n":1},{"n":4}]}`, math.NaN())
	if err != nil || v != 4 {
		t.Fatalf("index %v %v", v, err)
	}
	v, _, err = Apply([]Step{{Type: "xpath", Path: "/root/item[2]"}}, `<root><item>1</item><item>9</item></root>`, math.NaN())
	if err != nil || v != 9 {
		t.Fatalf("xpath %v %v", v, err)
	}
	v, _, err = Apply([]Step{{Type: "bool", Pattern: `ok`}}, "status=ok", math.NaN())
	if err != nil || v != 1 {
		t.Fatalf("bool %v %v", v, err)
	}
	_, prev, err := Apply([]Step{{Type: "change"}}, "10", math.NaN())
	if err == nil {
		t.Fatal("first change should wait for a previous sample")
	}
	v, prev, err = Apply([]Step{{Type: "change"}}, "14", prev)
	if err != nil || v != 4 || prev != 14 {
		t.Fatalf("delta %v prev %v err %v", v, prev, err)
	}
	if _, _, err = Apply([]Step{{Type: "nope"}}, "1", math.NaN()); err == nil {
		t.Fatal("accepted unknown step")
	}
}
