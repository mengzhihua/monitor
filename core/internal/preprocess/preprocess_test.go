package preprocess

import (
	"errors"
	"math"
	"testing"
	"time"
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

func TestZabbixPreprocessSteps(t *testing.T) {
	v, _, err := Apply([]Step{{Type: "replace", Pattern: ",", Replacement: "."}}, "1,5", math.NaN())
	if err != nil || v != 1.5 {
		t.Fatalf("replace %v %v", v, err)
	}
	v, _, err = Apply([]Step{{Type: "ltrim", Pattern: "x"}, {Type: "rtrim", Pattern: "x"}}, "xx12xx", math.NaN())
	if err != nil || v != 12 {
		t.Fatalf("trim sides %v %v", v, err)
	}
	v, _, err = Apply([]Step{{Type: "hex"}}, "0x10", math.NaN())
	if err != nil || v != 16 {
		t.Fatalf("hex %v %v", v, err)
	}
	v, _, err = Apply([]Step{{Type: "octal"}}, "075", math.NaN())
	if err != nil || v != 61 {
		t.Fatalf("octal %v %v", v, err)
	}
	v, _, err = Apply([]Step{{Type: "octal"}}, "0", math.NaN())
	if err != nil || v != 0 {
		t.Fatalf("octal zero %v %v", v, err)
	}
	v, _, err = Apply([]Step{{Type: "matches", Pattern: `^[0-9]+$`}}, "12", math.NaN())
	if err != nil || v != 12 {
		t.Fatalf("matches %v %v", v, err)
	}
	if _, _, err = Apply([]Step{{Type: "not_match", Pattern: `err`}}, "err", math.NaN()); err == nil {
		t.Fatal("not_match accepted a match")
	}
	min, max := 1.0, 10.0
	v, _, err = Apply([]Step{{Type: "in_range", Min: &min, Max: &max}}, "10", math.NaN())
	if err != nil || v != 10 {
		t.Fatalf("in_range %v %v", v, err)
	}
	if _, _, err = Apply([]Step{{Type: "in_range", Min: &min, Max: &max}}, "11", math.NaN()); err == nil {
		t.Fatal("in_range accepted 11")
	}
	v, prev, err := Apply([]Step{{Type: "discard_unchanged"}}, "4", math.NaN())
	if err != nil || v != 4 || prev != 4 {
		t.Fatalf("first unchanged %v %v %v", v, prev, err)
	}
	if _, next, err := Apply([]Step{{Type: "discard_unchanged"}}, "4", prev); !errors.Is(err, ErrUnchanged) || next != prev {
		t.Fatalf("repeat should discard, next=%v err=%v", next, err)
	}
	v, prev, err = Apply([]Step{{Type: "discard_unchanged"}}, "5", prev)
	if err != nil || v != 5 || prev != 5 {
		t.Fatalf("changed value %v %v %v", v, prev, err)
	}
}

func TestJavaScriptPreprocessIsBounded(t *testing.T) {
	v, _, err := Apply([]Step{{Type: "javascript", Script: "return Number(value) * 2"}}, "3", math.NaN())
	if err != nil || v != 6 {
		t.Fatalf("javascript %v %v", v, err)
	}
	started := time.Now()
	if _, _, err = Apply([]Step{{Type: "javascript", Script: "while (true) {}"}}, "1", math.NaN()); err == nil || time.Since(started) > 2*time.Second {
		t.Fatalf("loop err=%v elapsed=%s", err, time.Since(started))
	}
	if _, _, err = Apply([]Step{{Type: "javascript", Script: "return require('fs')"}}, "1", math.NaN()); err == nil {
		t.Fatal("javascript could call require")
	}
}
