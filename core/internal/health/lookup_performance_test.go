package health

import (
	"fmt"
	"io"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func lookupFixture(t testing.TB, dimensions int) (*Engine, *registry.Chart) {
	t.Helper()
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir(), Tiers: []tsdb.TierSpec{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{Hostname: "lookup-test", UpdateEvery: 1}, db)
	c := reg.AddChart(&registry.Chart{ID: "lookup.test"})
	for i := 0; i < dimensions; i++ {
		c.AddDimension(&registry.Dimension{ID: fmt.Sprintf("dim_%d", i), Name: fmt.Sprintf("alias_%d", i)})
	}
	return &Engine{db: db}, c
}

type countingLookupAnomaly struct {
	calls map[string]int
}

func (s *countingLookupAnomaly) Rate(string, string) (float64, bool) { return 0, false }

func (s *countingLookupAnomaly) RatesBetween(_, dimension string, _, _ int64) []float64 {
	s.calls[dimension]++
	return []float64{25, 75}
}

func TestLookupAnomalyReadsRequiredDimensions(t *testing.T) {
	e, c := lookupFixture(t, 3)
	source := &countingLookupAnomaly{calls: map[string]int{}}
	e.anomaly = source
	for _, percentage := range []bool{false, true} {
		source.calls = map[string]int{}
		l := &Lookup{Method: tsdb.GroupAverage, After: time.Minute, AnomalyBit: true,
			Dimensions: []string{"alias_0"}, Percentage: percentage}
		want, reads := 50.0, 1
		if percentage {
			want, reads = 100.0/3, 3
		}
		if got := e.lookup(c, l, time.Unix(1_700_000_000, 0)); math.Abs(got-want) > 1e-9 {
			t.Fatalf("percentage=%v: lookup=%v, want %v", percentage, got, want)
		}
		if len(source.calls) != reads || source.calls["dim_0"] != 1 {
			t.Fatalf("percentage=%v: series reads=%v, want %d including dim_0", percentage, source.calls, reads)
		}
	}
}

func TestLookupSelectedDimensions(t *testing.T) {
	e, c := lookupFixture(t, 3)
	now := time.Unix(1_700_000_000, 0)
	for i, offset := range []int64{-4, -3, -1, 0} {
		e.db.Append(registry.SeriesID(c.ID, "dim_0"), now.Unix()+offset, []float64{2, math.NaN(), 6, 4}[i])
		e.db.Append(registry.SeriesID(c.ID, "dim_1"), now.Unix()+offset, 10)
		e.db.Append(registry.SeriesID(c.ID, "dim_2"), now.Unix()+offset, math.NaN())
	}
	for _, tc := range []struct {
		lookup string
		want   float64
	}{
		{"average -10s of dim_0", 4},
		{"average -10s of alias_0", 4},
		{"average -10s of dim_0,alias_0", 4},
		{"average -10s", 14},
		{"average -10s of *", 14},
		{"average -10s percentage of dim_0", 400.0 / 14},
		{"average -10s percentage of alias_0", 400.0 / 14},
		{"average -10s percentage of *", 100},
		{"sum -10s of dim_0", 18}, // NaN samples still delimit the rate's interval.
		{"sum -10s of *", 68},
		{"sum -10s percentage of dim_0", 1800.0 / 68},
		{"min2max -10s of dim_0", 4},
		{"average -10s of missing", math.NaN()},
		{"average -10s percentage of missing", math.NaN()},
		{"sum -10s of dim_2", math.NaN()},
		{"sum -10s percentage of dim_2", math.NaN()},
		{"min2max -10s of dim_2", math.NaN()},
	} {
		t.Run(tc.lookup, func(t *testing.T) {
			l, err := ParseLookup(tc.lookup)
			if err != nil {
				t.Fatal(err)
			}
			got := e.lookup(c, l, now)
			if math.IsNaN(got) != math.IsNaN(tc.want) || (!math.IsNaN(got) && math.Abs(got-tc.want) > 1e-9) {
				t.Fatalf("lookup = %v, want %v", got, tc.want)
			}
		})
	}
	// Empty windows and all-NaN windows must remain undefined rather than zero.
	l, _ := ParseLookup("sum -10s of dim_0")
	if got := e.lookup(c, l, now.Add(time.Minute)); !math.IsNaN(got) {
		t.Fatalf("empty sum = %v, want NaN", got)
	}
}

// The full chart remains populated even when only one dimension is requested.
// This represents wide per-process/container charts without counting setup or
// WAL writes in the measurements. Percentage intentionally needs every series.
func BenchmarkLookupSelectedDimensions(b *testing.B) {
	for _, dimensions := range []int{1, 64} {
		for _, method := range []string{"average", "sum", "average_percentage"} {
			b.Run(fmt.Sprintf("%s/%d_dimensions", method, dimensions), func(b *testing.B) {
				e, c := lookupFixture(b, dimensions)
				now := time.Unix(1_700_000_000, 0)
				for d := 0; d < dimensions; d++ {
					id := registry.SeriesID(c.ID, fmt.Sprintf("dim_%d", d))
					for i := int64(0); i < 300; i++ {
						e.db.Append(id, now.Unix()-299+i, float64(i%30+1))
					}
				}
				spec := method + " -5m of dim_0"
				if method == "average_percentage" {
					spec = "average -5m percentage of dim_0"
				}
				l, err := ParseLookup(spec)
				if err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				for b.Loop() {
					if got := e.lookup(c, l, now); math.IsNaN(got) {
						b.Fatal("unexpected undefined result")
					}
				}
			})
		}
	}
}
