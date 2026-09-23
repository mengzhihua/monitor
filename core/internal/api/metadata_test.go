package api

import (
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

type countedAnomaly struct {
	fakeAnom
	calls atomic.Int64
}

func (a *countedAnomaly) Rate(chart, dim string) (float64, bool) {
	a.calls.Add(1)
	return a.fakeAnom.Rate(chart, dim)
}

func TestContextsPreserveMetadataWithoutAnomalyWork(t *testing.T) {
	src := &countedAnomaly{fakeAnom: fakeAnom{rate: map[string]float64{"test.a|used": 100}}}
	ts, reg := newTestServer(t, Options{Anomaly: src})
	for _, c := range []*registry.Chart{
		{ID: "test.a", Context: "test.shared", Title: "Shared", Priority: 300,
			Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "free"}}},
		{ID: "test.b", Context: "test.shared", Priority: 100,
			Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "extra"}}},
	} {
		reg.AddChart(c)
	}
	for _, sample := range []struct {
		chart string
		ts    int64
		vals  map[string]float64
	}{
		{"test.a", 1000, map[string]float64{"used": 1}},
		{"test.a", 1030, map[string]float64{"free": 2}},
		{"test.b", 990, map[string]float64{"used": 3}},
		{"test.b", 1010, map[string]float64{"extra": 4}},
	} {
		if err := reg.Collect(sample.chart, time.Unix(sample.ts, 0), sample.vals); err != nil {
			t.Fatal(err)
		}
	}
	var contexts struct {
		Contexts map[string]ctxInfo `json:"contexts"`
	}
	for _, version := range []string{"v1", "v2", "v3"} {
		if resp := getJSON(t, ts.URL+"/api/"+version+"/contexts", &contexts); resp.StatusCode != http.StatusOK {
			t.Fatalf("contexts status = %d", resp.StatusCode)
		}
		info := contexts.Contexts["test.shared"]
		if info.Priority != 100 || info.FirstEntry != 990 || info.LastEntry != 1030 ||
			!reflect.DeepEqual(info.Charts, []string{"test.a", "test.b"}) ||
			!reflect.DeepEqual(info.Dimensions, []string{"used", "extra", "free"}) {
			t.Fatalf("%s context = %+v", version, info)
		}
	}
	if calls := src.calls.Load(); calls != 0 {
		t.Fatalf("contexts performed %d unused anomaly queries", calls)
	}

	// The chart endpoints must still include rates and anomaly flags after the
	// context endpoint stops constructing that otherwise-discarded payload.
	type chartMetadata struct {
		FirstEntry int64 `json:"first_entry"`
		LastEntry  int64 `json:"last_entry"`
		Anomaly    bool  `json:"anomaly"`
		Dimensions []struct {
			ID      string  `json:"id"`
			Anomaly bool    `json:"anomaly"`
			Rate    float64 `json:"dimension_anomaly"`
		} `json:"dimensions"`
	}
	var charts struct {
		Charts map[string]chartMetadata `json:"charts"`
	}
	getJSON(t, ts.URL+"/api/v1/charts", &charts)
	var single chartMetadata
	getJSON(t, ts.URL+"/api/v1/chart?chart=test.a", &single)
	if !reflect.DeepEqual(charts.Charts["test.a"], single) || single.FirstEntry != 1000 || single.LastEntry != 1030 || !single.Anomaly ||
		len(single.Dimensions) != 2 || single.Dimensions[0].Rate != 100 || !single.Dimensions[0].Anomaly || single.Dimensions[1].Anomaly {
		t.Fatalf("chart metadata = %+v, list entry = %+v", single, charts.Charts["test.a"])
	}
}
