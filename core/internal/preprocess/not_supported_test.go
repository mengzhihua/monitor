package preprocess

import (
	"errors"
	"math"
	"testing"
)

func TestNotSupportedValue(t *testing.T) {
	steps := []Step{{Type: "not_supported", Pattern: `^down$`}}
	if _, _, err := Apply(steps, "down", math.NaN()); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("expected not supported, got %v", err)
	}
	v, _, err := Apply(steps, "5", math.NaN())
	if err != nil || v != 5 {
		t.Fatalf("numeric value %v %v", v, err)
	}
	if _, _, err := Apply([]Step{{Type: "not_supported", Pattern: "("}}, "down", math.NaN()); err == nil || errors.Is(err, ErrNotSupported) {
		t.Fatalf("broken pattern: %v", err)
	}
	if _, _, err := Apply([]Step{{Type: "not_supported"}}, "down", math.NaN()); err == nil {
		t.Fatal("accepted an empty pattern")
	}
}
