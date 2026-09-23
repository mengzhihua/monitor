package api

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

// A fixed anomaly source isolates the cost of reading metric history that an
// anomaly-only response discards. Real ML/HTTP behavior is covered separately.
func anomalyQueryFixture(t testing.TB, samples int) (*Server, *registry.Registry, *tsdb.Store) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir(), BlockSize: 300, Tiers: []tsdb.TierSpec{{Every: 60, BlockSize: 20}}, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{ID: "local", Hostname: "fixture", UpdateEvery: 1}, db)
	source := fakeAnom{rate: map[string]float64{}, hist: map[string][]float64{}}
	for c := 0; c < 2; c++ {
		chart := &registry.Chart{ID: fmt.Sprintf("fixture.%d", c), Context: "fixture.context", Units: "bytes"}
		for d := 0; d < 4; d++ {
			id := fmt.Sprintf("dim%d", d)
			chart.Dimensions = append(chart.Dimensions, &registry.Dimension{ID: id, Name: "name-" + id, Hidden: d == 3})
			source.rate[chart.ID+"|"+id] = 100
			source.hist[chart.ID+"|"+id] = []float64{0, 100}
		}
		reg.AddChart(chart)
		for i := 0; i < samples; i++ {
			values := map[string]float64{}
			for d := 0; d < 4; d++ {
				values[fmt.Sprintf("dim%d", d)] = float64(i + d + c)
			}
			if err := reg.Collect(chart.ID, time.Unix(1700000000+int64(i), 0), values); err != nil {
				t.Fatal(err)
			}
		}
	}
	sched := collect.NewScheduler(reg, log, collect.Options{Names: []string{"none"}})
	srv, err := New(reg, db, sched, Options{Logger: log, Anomaly: source})
	if err != nil {
		t.Fatal(err)
	}
	return srv, reg, db
}

type anomalyQueryReader struct {
	tsdb.Reader
	calls int
}

func (r *anomalyQueryReader) QueryAggregated(id string, tier int, after, before int64, points int, fn tsdb.GroupFunc) (tsdb.Result, error) {
	r.calls++
	return r.Reader.QueryAggregated(id, tier, after, before, points, fn)
}

func TestAnomalyQuerySkipsMetricReads(t *testing.T) {
	srv, reg, db := anomalyQueryFixture(t, 650)
	reader := &anomalyQueryReader{Reader: db}
	v := &view{reg: reg, db: reader}
	for _, tier := range []string{"0", "1", "auto"} {
		for _, selection := range []url.Values{{"chart": {"fixture.0"}}, {"context": {"fixture.context"}}, {"chart": {"fixture.0"}, "dimensions": {"name-dim3"}}} {
			q := url.Values{"after": {"1700000001"}, "before": {"1700000649"}, "points": {"37"}, "tier": {tier}, "options": {"anomaly-bit"}}
			for k, value := range selection {
				q[k] = value
			}
			reader.calls = 0
			got, _, message := srv.queryData(v, q, 1)
			if message != "" {
				t.Fatal(message)
			}
			if reader.calls != 0 {
				t.Fatalf("anomaly-only query read metric data %d times", reader.calls)
			}
			grid, err := db.QueryAggregated("fixture.0|dim0", got.Tier, 1700000001, 1700000649, 37, tsdb.GroupAverage)
			if err != nil {
				t.Fatal(err)
			}
			if got.After != grid.After || got.Before != grid.Before || got.ViewUpdateEvery != grid.Step || len(got.Rows) != len(grid.Times) || got.Units != "%" {
				t.Fatalf("time grid/units changed: %+v", got)
			}
			for i, row := range got.Rows {
				if row[0] != grid.Times[i] {
					t.Fatal("timestamp changed")
				}
				for _, value := range row[1:] {
					if value != float64(50) {
						t.Fatalf("unexpected anomaly value %v", value)
					}
				}
			}
			if selection.Get("dimensions") != "" && (len(got.DimensionIDs) != 1 || got.DimensionIDs[0] != "dim3") {
				t.Fatal("explicit hidden dimension lost")
			}
			q.Del("options")
			reader.calls = 0
			if _, _, message := srv.queryData(v, q, 1); message != "" || reader.calls == 0 {
				t.Fatal("ordinary query bypassed metric data")
			}
		}
	}
	for _, query := range []string{
		"chart=fixture.0&tier=999&options=anomaly-bit",
		"chart=missing&options=anomaly-bit",
	} {
		q, _ := url.ParseQuery(query)
		if _, code, message := srv.queryData(v, q, 1); message == "" || (code != http.StatusBadRequest && code != http.StatusNotFound) {
			t.Fatal("invalid request accepted")
		}
	}
	q := url.Values{"chart": {"fixture.0"}, "dimensions": {"missing"}, "options": {"anomaly_bit"}}
	got, _, message := srv.queryData(v, q, 1)
	if message != "" || len(got.Rows) != 0 || len(got.DimensionIDs) != 0 {
		t.Fatal("empty dimension selection changed")
	}
	srv.opt.Anomaly = nil
	q = url.Values{"chart": {"fixture.0"}, "after": {"1700000001"}, "before": {"1700000649"}, "points": {"37"}, "options": {"anomaly_bit"}}
	got, _, message = srv.queryData(v, q, 1)
	if message != "" {
		t.Fatal(message)
	}
	for _, row := range got.Rows {
		for _, value := range row[1:] {
			if value != float64(0) {
				t.Fatal("missing anomaly source changed")
			}
		}
	}
}

func BenchmarkAnomalyDataQuery(b *testing.B) {
	srv, _, _ := anomalyQueryFixture(b, 3600)
	for _, kind := range []string{"metric", "anomaly"} {
		b.Run(kind, func(b *testing.B) {
			options := "all-dimensions"
			if kind == "anomaly" {
				options += ",anomaly-bit"
			}
			request := httptest.NewRequest(http.MethodGet, "/api/v1/data?context=fixture.context&after=1700000000&before=1700003599&points=600&tier=0&options="+options, nil)
			writer := discardResponse{header: make(http.Header)}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				srv.handleData(writer, request)
			}
		})
	}
}
