package preprocess

import (
	"errors"
	"math"
	"testing"
)

func TestThrottleDropsEarlyChanges(t *testing.T) {
	steps := []Step{{Type: "throttle", Seconds: 10}}
	value, prev, kept, err := ApplyTimed(steps, "4", math.NaN(), 0, 100)
	if err != nil || value != 4 || prev != 4 || kept != 100 {
		t.Fatalf("first %v %v %v %v", value, prev, kept, err)
	}
	if _, _, nextKept, err := ApplyTimed(steps, "9", prev, kept, 109); !errors.Is(err, ErrThrottled) || nextKept != kept {
		t.Fatalf("inside window kept=%v err=%v", nextKept, err)
	}
	value, prev, kept, err = ApplyTimed(steps, "9", prev, kept, 110)
	if err != nil || value != 9 || prev != 9 || kept != 110 {
		t.Fatalf("boundary %v %v %v %v", value, prev, kept, err)
	}
	if _, _, nextKept, err := ApplyTimed(steps, "1", prev, kept, 100); !errors.Is(err, ErrThrottled) || nextKept != kept {
		t.Fatalf("clock step back kept=%v err=%v", nextKept, err)
	}
	if _, _, _, err := ApplyTimed([]Step{{Type: "throttle", Seconds: 0}}, "1", math.NaN(), 0, 1); err == nil {
		t.Fatal("accepted a zero period")
	}
	if err := Validate([]Step{{Type: "throttle", Seconds: 86401}}); err == nil {
		t.Fatal("accepted a period longer than a day")
	}
}
