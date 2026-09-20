package registry

import (
	"math"
	"testing"
	"time"
)

type memSink struct{ got map[string][]float64 }

func (m *memSink) Append(id string, _ int64, v float64) {
	if m.got == nil {
		m.got = map[string][]float64{}
	}
	m.got[id] = append(m.got[id], v)
}

func newReg(sink Sink) *Registry {
	return New(&Host{ID: "h", Hostname: "h", UpdateEvery: 1}, sink)
}

func TestAddChartKeepsDimensions(t *testing.T) {
	r := newReg(nil)
	c := r.AddChart(&Chart{ID: "a.b", Dimensions: []*Dimension{{ID: "x"}, {ID: "y", Multiplier: 8, Divisor: 1000}}})
	if len(c.Dims()) != 2 {
		t.Fatalf("dims = %d, want 2", len(c.Dims()))
	}
	if d := c.Dimension("y"); d == nil || d.Multiplier != 8 || d.Divisor != 1000 || d.Name != "y" || d.Algorithm != Absolute {
		t.Fatalf("bad dimension defaults: %+v", d)
	}
	if again := r.AddChart(&Chart{ID: "a.b"}); again != c {
		t.Fatal("AddChart should be idempotent by id")
	}
	if c.Type != Line || c.Context != "a.b" || c.UpdateEvery != 1 {
		t.Fatalf("chart defaults not applied: %+v", c)
	}
}

func TestIncremental(t *testing.T) {
	s := &memSink{}
	r := newReg(s)
	r.AddChart(&Chart{ID: "net.eth0", Dimensions: []*Dimension{
		{ID: "rx", Algorithm: Incremental, Multiplier: 8, Divisor: 1000},
	}})
	t0 := time.Unix(1000, 0)
	_ = r.Collect("net.eth0", t0, map[string]float64{"rx": 1000})                    // first sample: no output
	_ = r.Collect("net.eth0", t0.Add(time.Second), map[string]float64{"rx": 3000})   // +2000 B/s → 16 kbit/s
	_ = r.Collect("net.eth0", t0.Add(3*time.Second), map[string]float64{"rx": 7000}) // +4000 over 2s → 16 kbit/s
	_ = r.Collect("net.eth0", t0.Add(4*time.Second), map[string]float64{"rx": 100})  // counter reset → skipped
	_ = r.Collect("net.eth0", t0.Add(5*time.Second), map[string]float64{"rx": 1100}) // +1000 → 8 kbit/s
	got := s.got[SeriesID("net.eth0", "rx")]
	want := []float64{16, 16, 8}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if err := r.Collect("nope", t0, nil); err == nil {
		t.Fatal("unknown chart should error")
	}
}

func TestPercentageOfIncrementalRow(t *testing.T) {
	s := &memSink{}
	r := newReg(s)
	r.AddChart(&Chart{ID: "system.cpu", Dimensions: []*Dimension{
		{ID: "user", Algorithm: PercentageOfIncrementalRow},
		{ID: "idle", Algorithm: PercentageOfIncrementalRow, Hidden: true},
	}})
	t0 := time.Unix(1000, 0)
	_ = r.Collect("system.cpu", t0, map[string]float64{"user": 100, "idle": 900})
	_ = r.Collect("system.cpu", t0.Add(time.Second), map[string]float64{"user": 125, "idle": 975})
	// deltas: user 25, idle 75 → 25% / 75%
	if v := s.got[SeriesID("system.cpu", "user")]; len(v) != 1 || math.Abs(v[0]-25) > 1e-9 {
		t.Fatalf("user = %v, want [25]", v)
	}
	if v := s.got[SeriesID("system.cpu", "idle")]; len(v) != 1 || math.Abs(v[0]-75) > 1e-9 {
		t.Fatalf("idle = %v, want [75]", v)
	}
	ts, last := r.charts["system.cpu"].LastValues()
	if ts != t0.Unix()+1 || last["user"] != 25 {
		t.Fatalf("LastValues = %d %v", ts, last)
	}
}

func TestSubscribe(t *testing.T) {
	r := newReg(nil)
	r.AddChart(&Chart{ID: "x", Dimensions: []*Dimension{{ID: "a"}}})
	var gotChart string
	var gotVals map[string]float64
	r.Subscribe(func(chartID string, _ int64, values map[string]float64) { gotChart, gotVals = chartID, values })
	_ = r.Collect("x", time.Now(), map[string]float64{"a": 3, "unknown": 1})
	if gotChart != "x" || gotVals["a"] != 3 || len(gotVals) != 1 {
		t.Fatalf("subscriber got %s %v", gotChart, gotVals)
	}
}
