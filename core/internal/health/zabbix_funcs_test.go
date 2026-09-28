package health

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestParseLookupZabbix(t *testing.T) {
	type want struct {
		kind    string
		horizon time.Duration
		target  float64
		op      string
		val     float64
		dims    int
	}
	cases := []struct {
		in   string
		want want
		err  string
	}{
		{"nodata -5m", want{kind: "nodata"}, ""},
		{"FIRST -5m of used", want{kind: "first", dims: 1}, ""},
		{"change -5m absolute", want{kind: "change"}, ""},
		{"stddev -5m", want{kind: "stddev"}, ""},
		{"count -5m", want{kind: "count"}, ""},
		{"count -5m gt 3.5", want{kind: "count", op: "gt", val: 3.5}, ""},
		{"count -5m ne 0 of used,free", want{kind: "count", op: "ne", dims: 2}, ""},
		{"trendavg -1d", want{kind: "trendavg"}, ""},
		{"trendmin -1d", want{kind: "trendmin"}, ""},
		{"trendmax -1d", want{kind: "trendmax"}, ""},
		{"trendsum -1d", want{kind: "trendsum"}, ""},
		{"trendcount -1d", want{kind: "trendcount"}, ""},
		{"forecast -1h horizon 30m", want{kind: "forecast", horizon: 30 * time.Minute}, ""},
		{"timeleft -1h target 0", want{kind: "timeleft"}, ""},
		{"timeleft -1h target 95.5", want{kind: "timeleft", target: 95.5}, ""},
		{"forecast -1h", want{}, "horizon"},
		{"forecast -1h horizon nope", want{}, "bad horizon"},
		{"timeleft -1h", want{}, "target"},
		{"timeleft -1h target x", want{}, "bad target"},
		{"average -5m gt 1", want{}, "only applies to count"},
		{"count -5m gt", want{}, "missing value"},
		{"count -5m gt x", want{}, "bad count operand"},
	}
	for _, c := range cases {
		l, err := ParseLookup(c.in)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("%q: want error containing %q, got %v", c.in, c.err, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if l.Kind != c.want.kind || l.Horizon != c.want.horizon || l.Target != c.want.target ||
			l.CountOp != c.want.op || l.CountVal != c.want.val || len(l.Dimensions) != c.want.dims {
			t.Fatalf("%q: %+v", c.in, l)
		}
	}
}

const zRule = `
alarms:
  - name: z_test
    on: system.ram
    lookup: LOOKUP
    every: 1s
    warn: '$this >= 0'
`

func lookupValue(t *testing.T, lookup string, seed func(reg *registry.Registry, now time.Time), now time.Time) float64 {
	t.Helper()
	e, reg := newTestEngine(t, strings.Replace(zRule, "LOOKUP", lookup, 1))
	defer e.Close()
	if seed != nil {
		seed(reg, now)
	}
	l, err := ParseLookup(lookup)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := reg.Chart("system.ram")
	if !ok {
		t.Fatal("chart missing")
	}
	return e.lookup(c, l, now)
}

func TestLookupNodata(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	if v := lookupValue(t, "nodata -5m", nil, now); v != 1 {
		t.Fatalf("empty window: got %v want 1", v)
	}
	v := lookupValue(t, "nodata -5m", func(reg *registry.Registry, now time.Time) {
		reg.Collect("system.ram", now.Add(-time.Minute), map[string]float64{"used": 10})
	}, now)
	if v != 0 {
		t.Fatalf("with data: got %v want 0", v)
	}
}

func TestLookupFirstChangeStddevCount(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	seed := func(reg *registry.Registry, now time.Time) {
		for i := 0; i < 5; i++ {
			reg.Collect("system.ram", now.Add(time.Duration(i-4)*time.Second), map[string]float64{"used": float64(i)})
		}
	}
	if v := lookupValue(t, "first -5m of used", seed, now); v != 0 {
		t.Fatalf("first: got %v want 0", v)
	}
	if v := lookupValue(t, "change -5m of used", seed, now); v != 4 {
		t.Fatalf("change: got %v want 4", v)
	}
	if v := lookupValue(t, "stddev -5m of used", seed, now); math.Abs(v-math.Sqrt2) > 1e-9 {
		t.Fatalf("stddev: got %v want %v", v, math.Sqrt2)
	}
	if v := lookupValue(t, "count -5m of used", seed, now); v != 5 {
		t.Fatalf("count: got %v want 5", v)
	}
	if v := lookupValue(t, "count -5m gt 2 of used", seed, now); v != 2 {
		t.Fatalf("count gt 2: got %v want 2", v)
	}
}

func TestLookupForecastTimeleft(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	// Perfectly linear ramp: value = seconds elapsed (slope 1/s over 60 s).
	up := func(reg *registry.Registry, now time.Time) {
		for i := 0; i <= 60; i++ {
			reg.Collect("system.ram", now.Add(time.Duration(i-60)*time.Second), map[string]float64{"used": float64(i)})
		}
	}
	if v := lookupValue(t, "forecast -2m horizon 30s of used", up, now); math.Abs(v-90) > 1e-6 {
		t.Fatalf("forecast: got %v want 90", v)
	}
	// Falling to zero: value 60 at t-60s … 0 at now, hits target 0 right now.
	down := func(reg *registry.Registry, now time.Time) {
		for i := 0; i <= 60; i++ {
			reg.Collect("system.ram", now.Add(time.Duration(i-60)*time.Second), map[string]float64{"used": float64(60 - i)})
		}
	}
	if v := lookupValue(t, "timeleft -2m target 0 of used", down, now); math.Abs(v) > 1e-6 {
		t.Fatalf("timeleft: got %v want ~0", v)
	}
	// Moving away from the target must yield the finite sentinel, not Inf.
	if v := lookupValue(t, "timeleft -2m target 0 of used", up, now); v != timeLeftSentinel {
		t.Fatalf("timeleft away: got %v want %v", v, timeLeftSentinel)
	}
}

func TestLookupTrendFallback(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	seed := func(reg *registry.Registry, now time.Time) {
		for i := 0; i < 5; i++ {
			reg.Collect("system.ram", now.Add(time.Duration(i-4)*time.Second), map[string]float64{"used": float64(i)})
		}
	}
	// No rollup buckets exist yet → tier-0 fallback, average of 0..4.
	if v := lookupValue(t, "trendavg -5m of used", seed, now); v != 2 {
		t.Fatalf("trendavg fallback: got %v want 2", v)
	}
	if v := lookupValue(t, "trendcount -5m of used", seed, now); v != 5 {
		t.Fatalf("trendcount fallback: got %v want 5", v)
	}
}

func TestMacros(t *testing.T) {
	spec := RuleSpec{
		Name: "m", On: "system.ram",
		Lookup: "average {$WINDOW} of used",
		Warn:   "$this > {$THRESH}",
		Macros: map[string]string{"THRESH": "42"},
	}
	r, err := CompileWith(spec, "t", map[string]string{"WINDOW": "-5m", "THRESH": "99"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Lookup.After != 5*time.Minute {
		t.Fatalf("global macro not applied: %v", r.Lookup.After)
	}
	if r.Spec.Lookup != spec.Lookup || r.Spec.Warn != spec.Warn {
		t.Fatal("Spec mutated by macro expansion")
	}
	// Rule-level beats global: warn fires at $this > 42, not > 99.
	fires := r.Warn.Eval(func(name string) (float64, bool) {
		if name == "this" {
			return 50, true
		}
		return 0, true
	})
	if fires == 0 {
		t.Fatal("rule-level macro value not applied")
	}
	// Hash depends on the original text only.
	r2, err := CompileWith(spec, "t", map[string]string{"WINDOW": "-1h", "THRESH": "7"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Hash() != r2.Hash() {
		t.Fatal("Hash changed with macro values")
	}
	// Unresolved reference fails.
	if _, err := CompileWith(RuleSpec{Name: "m", On: "x", Warn: "$this > {$NOPE}"}, "t", nil); err == nil || !strings.Contains(err.Error(), "unknown macro {$NOPE}") {
		t.Fatalf("want unknown macro error, got %v", err)
	}
}
