package tsdb

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

func TestGorillaRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	n := 5000
	ts := make([]int64, n)
	vals := make([]float64, n)
	base := time.Now().Unix()
	v := 42.0
	for i := 0; i < n; i++ {
		base += 1
		if i%97 == 0 {
			base += int64(r.Intn(5)) // occasional gaps
		}
		ts[i] = base
		switch {
		case i%13 == 0:
			v = float64(r.Intn(100))
		case i%7 == 0:
			// unchanged
		default:
			v += r.Float64()*2 - 1
		}
		vals[i] = v
	}
	vals[10] = 0
	vals[11] = -0.0
	vals[12] = math.MaxFloat64
	vals[13] = math.SmallestNonzeroFloat64
	buf := encodeBlock(ts, vals)
	gotTS, gotV, err := decodeBlock(buf, n)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotTS) != n {
		t.Fatalf("len %d != %d", len(gotTS), n)
	}
	for i := range ts {
		if gotTS[i] != ts[i] || math.Float64bits(gotV[i]) != math.Float64bits(vals[i]) {
			t.Fatalf("mismatch at %d: (%d,%v) != (%d,%v)", i, gotTS[i], gotV[i], ts[i], vals[i])
		}
	}
	bps := float64(len(buf)*8) / float64(n)
	t.Logf("compressed to %.2f bits/sample", bps)
	if bps > 64 {
		t.Errorf("no compression gain: %.2f bits/sample", bps)
	}
}

func TestGorillaCompressesTypicalMetrics(t *testing.T) {
	// per-second samples of a slowly changing integer-ish metric (e.g. CPU %)
	n := 3600
	ts := make([]int64, n)
	vals := make([]float64, n)
	for i := 0; i < n; i++ {
		ts[i] = 1_700_000_000 + int64(i)
		vals[i] = float64(20 + (i/30)%5)
	}
	buf := encodeBlock(ts, vals)
	bps := float64(len(buf)*8) / float64(n)
	t.Logf("typical metric: %.2f bits/sample", bps)
	if bps > 8 {
		t.Errorf("expected < 1 byte/sample, got %.2f bits", bps)
	}
}

func TestStoreFlushReloadQuery(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, BlockSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	start := int64(1_700_000_000)
	for i := int64(0); i < 250; i++ {
		s.Append("system.cpu|user", start+i, float64(i))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(Options{Dir: dir, BlockSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pts, err := s.Query("system.cpu|user", start+50, start+199)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 150 {
		t.Fatalf("got %d points, want 150", len(pts))
	}
	if pts[0].TS != start+50 || pts[0].Value != 50 || pts[149].TS != start+199 {
		t.Fatalf("bad bounds: %+v .. %+v", pts[0], pts[149])
	}
	first, last, ok := s.Bounds("system.cpu|user")
	if !ok || first != start || last != start+249 {
		t.Fatalf("bounds %d %d %v", first, last, ok)
	}
}

func TestAggregate(t *testing.T) {
	pts := []Point{{TS: 101, Value: 1}, {TS: 102, Value: 3}, {TS: 103, Value: 5}, {TS: 104, Value: 7}}
	res := Aggregate([][]Point{pts}, 100, 104, 2, GroupAverage)
	if res.Step != 2 || len(res.Times) != 2 || res.Times[0] != 102 || res.Times[1] != 104 {
		t.Fatalf("step=%d times=%v", res.Step, res.Times)
	}
	if res.Values[0][0] != 2 || res.Values[0][1] != 6 {
		t.Fatalf("values %v", res.Values[0])
	}
	res = Aggregate([][]Point{pts}, 100, 106, 3, GroupMax)
	if len(res.Times) != 3 || res.Values[0][1] != 7 || !math.IsNaN(res.Values[0][2]) {
		t.Fatalf("expected NaN for empty bucket, got %v (times %v)", res.Values[0], res.Times)
	}
}
