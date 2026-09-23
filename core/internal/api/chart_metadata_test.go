package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/hub"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

// Assert the actual JSON contract, including present false/zero/null values,
// independently of the Go type used to construct a metadata response.
func TestChartMetadataJSONContract(t *testing.T) {
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{UpdateEvery: 2}, db)
	c := reg.AddChart(&registry.Chart{ID: "test.chart", Context: "test.context", Family: "test", Title: "Title", Units: "bytes",
		Type: registry.Stacked, Priority: 42, Plugin: "fixture", Module: "metadata",
		Dimensions: []*registry.Dimension{{ID: "zero", Multiplier: 2, Divisor: 3}, {ID: "high", Hidden: true}, {ID: "flag"}, {ID: "normal"}}})
	if err := reg.Collect(c.ID, time.Unix(100, 0), map[string]float64{"zero": 1}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Collect(c.ID, time.Unix(120, 0), map[string]float64{"normal": 1}); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(chartJSON(c, db, map[string]bool{"test.chart|flag": true},
		fakeAnom{rate: map[string]float64{"test.chart|zero": 0, "test.chart|high": 50}}))
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	const expected = `{
		"id":"test.chart","context":"test.context","family":"test","title":"Title","units":"bytes",
		"chart_type":"stacked","priority":42,"update_every":2,"plugin":"fixture","module":"metadata","labels":null,
		"first_entry":100,"last_entry":120,"anomaly":true,
		"dimensions":[
			{"id":"zero","name":"zero","algorithm":"absolute","multiplier":2,"divisor":3,"hidden":false,"dimension_anomaly":0},
			{"id":"high","name":"high","algorithm":"absolute","multiplier":1,"divisor":1,"hidden":true,"dimension_anomaly":50,"anomaly":true},
			{"id":"flag","name":"flag","algorithm":"absolute","multiplier":1,"divisor":1,"hidden":false,"anomaly":true},
			{"id":"normal","name":"normal","algorithm":"absolute","multiplier":1,"divisor":1,"hidden":false}
		]}`
	if err := json.Unmarshal([]byte(expected), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("chart JSON = %s; want %s", encoded, expected)
	}
	empty := reg.AddChart(&registry.Chart{ID: "empty"})
	encoded, err = json.Marshal(chartJSON(empty, db, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got["dimensions"], []any{}) || got["anomaly"] != false || got["first_entry"] != float64(0) || got["last_entry"] != float64(0) {
		t.Fatalf("empty chart contract = %s", encoded)
	}
}

func TestChartMetadataRefreshesDefinitionsAndBounds(t *testing.T) {
	ts, reg := newTestServer(t, Options{})
	read := func() map[string]any {
		t.Helper()
		var list struct{ Charts map[string]map[string]any }
		if resp := getJSON(t, ts.URL+"/api/v1/charts", &list); resp.StatusCode != http.StatusOK {
			t.Fatalf("charts: %d", resp.StatusCode)
		}
		var single map[string]any
		if resp := getJSON(t, ts.URL+"/api/v1/chart?chart=system.ram", &single); resp.StatusCode != http.StatusOK {
			t.Fatalf("chart: %d", resp.StatusCode)
		}
		if !reflect.DeepEqual(list.Charts["system.ram"], single) {
			t.Fatalf("list differs from single chart: %v vs %v", list.Charts["system.ram"], single)
		}
		return single
	}
	initial := read()
	if initial["first_entry"] != float64(0) || len(initial["dimensions"].([]any)) != 2 {
		t.Fatalf("initial metadata: %v", initial)
	}
	c, _ := reg.Chart("system.ram")
	c.AddDimension(&registry.Dimension{ID: "added", Hidden: true})
	if err := reg.Collect(c.ID, time.Unix(100, 0), map[string]float64{"used": 10, "added": 20}); err != nil {
		t.Fatal(err)
	}
	added := read()
	if len(added["dimensions"].([]any)) != 3 || added["first_entry"] != float64(100) || added["last_entry"] != float64(100) {
		t.Fatalf("added dimension/sample missing: %v", added)
	}
	reg.ReplaceChart(&registry.Chart{ID: c.ID, Title: "Replacement", Labels: map[string]string{"source": "new"},
		Dimensions: []*registry.Dimension{{ID: "free", Name: "Remaining"}}})
	if err := reg.Collect(c.ID, time.Unix(130, 0), map[string]float64{"free": 30}); err != nil {
		t.Fatal(err)
	}
	replaced := read()
	dims := replaced["dimensions"].([]any)
	if replaced["title"] != "Replacement" || replaced["first_entry"] != float64(130) || replaced["last_entry"] != float64(130) ||
		len(dims) != 1 || dims[0].(map[string]any)["name"] != "Remaining" || replaced["labels"].(map[string]any)["source"] != "new" {
		t.Fatalf("replacement metadata stale: %v", replaced)
	}
	reg.RemoveChart(c.ID)
	var list struct{ Charts map[string]any }
	getJSON(t, ts.URL+"/api/v1/charts", &list)
	if _, ok := list.Charts[c.ID]; ok {
		t.Fatal("removed chart remains in response")
	}
	if resp := getJSON(t, ts.URL+"/api/v1/chart?chart=system.ram", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("removed chart: %d", resp.StatusCode)
	}
}

func TestChartMetadataScopesIdenticalIDsToNode(t *testing.T) {
	srv, nodes := newInfoHub(t, hub.Options{})
	for i, id := range []string{"node-a", "node-b"} {
		_, err := nodes.AcceptReplica(registry.Host{ID: id, Hostname: id, UpdateEvery: i + 1},
			[]*registry.Chart{{ID: "same.chart", Title: id, Dimensions: []*registry.Dimension{{ID: id}}}},
			[]hub.ReplicaSample{{Chart: "same.chart", T: int64(100 + i), V: map[string]float64{id: 1}}}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	// Alternate nodes and repeat to catch accidental response reuse across IDs.
	for _, id := range []string{"node-a", "node-b", "node-a"} {
		var got struct {
			Node        string
			Hostname    string
			UpdateEvery int `json:"update_every"`
			Charts      map[string]chartMetadata
		}
		if resp := getJSON(t, ts.URL+"/api/v1/charts?node="+id, &got); resp.StatusCode != http.StatusOK {
			t.Fatalf("node %s: %d", id, resp.StatusCode)
		}
		c := got.Charts["same.chart"]
		wantTime, wantEvery := int64(100), 1
		if id == "node-b" {
			wantTime, wantEvery = 101, 2
		}
		if got.Node != id || got.Hostname != id || got.UpdateEvery != wantEvery || len(got.Charts) != 1 ||
			c.Title != id || c.FirstEntry != wantTime || c.LastEntry != wantTime || len(c.Dimensions) != 1 || c.Dimensions[0].ID != id {
			t.Fatalf("node %s: scoped metadata = %+v", id, got)
		}
	}
}
