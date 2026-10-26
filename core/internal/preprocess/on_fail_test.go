package preprocess

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestPreprocessOnFail(t *testing.T) {
	missing := []Step{{Type: "regex", Pattern: `ready ([0-9]+)`, OnFail: "discard"}}
	if _, _, err := Apply(missing, "down", math.NaN()); !errors.Is(err, ErrDiscard) {
		t.Fatalf("discard: %v", err)
	}
	fallback := []Step{{Type: "regex", Pattern: `ready ([0-9]+)`, OnFail: "value", FailValue: "0"}}
	v, prev, err := Apply(fallback, "down", math.NaN())
	if err != nil || v != 0 || prev != 0 {
		t.Fatalf("value fallback %v %v %v", v, prev, err)
	}
	v, _, err = Apply([]Step{
		{Type: "regex", Pattern: `ready ([0-9]+)`, OnFail: "value", FailValue: "0"},
		{Type: "multiplier", Factor: 2},
	}, "ready 4", math.NaN())
	if err != nil || v != 8 {
		t.Fatalf("later step still runs after a match %v %v", v, err)
	}
	custom := []Step{{Type: "in_range", Min: floatPtr(1), Max: floatPtr(2), OnFail: "error", FailValue: "sensor down"}}
	if _, _, err = Apply(custom, "9", math.NaN()); err == nil || !strings.Contains(err.Error(), "sensor down") {
		t.Fatalf("custom error: %v", err)
	}
	if _, _, err = Apply([]Step{{Type: "regex", Pattern: `([0-9]+)`}}, "down", math.NaN()); err == nil || errors.Is(err, ErrDiscard) {
		t.Fatalf("default failure should stay an error: %v", err)
	}
	if _, _, err = Apply([]Step{{Type: "regex", Pattern: `(`, OnFail: "discard"}}, "down", math.NaN()); err == nil || errors.Is(err, ErrDiscard) {
		t.Fatalf("a broken pattern should still fail: %v", err)
	}
	if _, _, err = Apply([]Step{{Type: "regex", Pattern: `([0-9]+)`, OnFail: "keep"}}, "down", math.NaN()); err == nil || !strings.Contains(err.Error(), "on_fail") {
		t.Fatalf("bad mode: %v", err)
	}
	if _, _, err = Apply([]Step{{Type: "regex", Pattern: `([0-9]+)`, OnFail: "value"}}, "down", math.NaN()); err == nil {
		t.Fatal("value mode without fail_value")
	}
}

func floatPtr(v float64) *float64 { return &v }
