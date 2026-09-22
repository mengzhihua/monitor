package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

type fakeAnom struct {
	rate map[string]float64
	hist map[string][]float64
}

func (f fakeAnom) Rate(chart, dim string) (float64, bool) {
	v, ok := f.rate[chart+"|"+dim]
	return v, ok
}

func (f fakeAnom) RatesBetween(chart, dim string, after, before int64) []float64 {
	return f.hist[chart+"|"+dim]
}

func TestAPIV3AndGroupByDimension(t *testing.T) {
	ts, reg := newTestServer(t, Options{Version: "test"})
	now := time.Now().Truncate(time.Second)
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("cpu.cpu%d", i)
		reg.AddChart(&registry.Chart{ID: id, Context: "cpu.cpu", Family: "utilization", Title: "Core", Units: "%",
			Dimensions: []*registry.Dimension{{ID: "user"}, {ID: "system"}}})
		for j := 0; j < 10; j++ {
			_ = reg.Collect(id, now.Add(time.Duration(j-9)*time.Second), map[string]float64{"user": float64(10 + i*5), "system": 2})
		}
	}
	after, before := now.Unix()-10, now.Unix()

	var v3 struct {
		API          int      `json:"api"`
		GroupBy      string   `json:"group_by"`
		DimensionIDs []string `json:"dimension_ids"`
		Result       struct {
			Data [][]*float64 `json:"data"`
		} `json:"result"`
	}
	getJSON(t, ts.URL+fmt.Sprintf("/api/v3/data?context=cpu.cpu&group_by=dimension&after=%d&before=%d", after, before), &v3)
	if v3.API != 3 {
		t.Fatalf("v3 api = %d", v3.API)
	}
	if v3.GroupBy != "dimension" {
		t.Fatalf("group_by = %q", v3.GroupBy)
	}
	if len(v3.DimensionIDs) != 2 {
		t.Fatalf("dims = %v", v3.DimensionIDs)
	}
	last := v3.Result.Data[len(v3.Result.Data)-1]
	if last[1] == nil || *last[1] != 25 || last[2] == nil || *last[2] != 4 {
		t.Fatalf("group_by=dimension last = %v", last)
	}

	var nd struct {
		API          int      `json:"api"`
		GroupBy      string   `json:"group_by"`
		DimensionIDs []string `json:"dimension_ids"`
		Result       struct {
			Data [][]*float64 `json:"data"`
		} `json:"result"`
	}
	getJSON(t, ts.URL+fmt.Sprintf("/api/v3/data?context=cpu.cpu&group_by=node,dimension&after=%d&before=%d", after, before), &nd)
	if nd.API != 3 || nd.GroupBy != "node,dimension" {
		t.Fatalf("node,dimension meta api=%d group=%q", nd.API, nd.GroupBy)
	}
	if len(nd.DimensionIDs) != 2 || nd.DimensionIDs[0] != "test.user" || nd.DimensionIDs[1] != "test.system" {
		t.Fatalf("node,dimension ids = %v", nd.DimensionIDs)
	}
	ndLast := nd.Result.Data[len(nd.Result.Data)-1]
	if ndLast[1] == nil || *ndLast[1] != 25 || ndLast[2] == nil || *ndLast[2] != 4 {
		t.Fatalf("node,dimension last = %v", derefRow(ndLast))
	}

	var info struct {
		API int `json:"api"`
	}
	getJSON(t, ts.URL+"/api/v3/info", &info)
	if info.API != 3 {
		t.Fatalf("v3 info api = %d", info.API)
	}
	getJSON(t, ts.URL+"/api/v3/contexts", &info)
	if info.API != 3 {
		t.Fatalf("v3 contexts api = %d", info.API)
	}
	getJSON(t, ts.URL+"/api/v3/nodes", &info)
	if info.API != 3 {
		t.Fatalf("v3 nodes api = %d", info.API)
	}
	var ctx struct {
		API     int    `json:"api"`
		ID      string `json:"id"`
		Context struct {
			Charts []string `json:"charts"`
		} `json:"context"`
	}
	getJSON(t, ts.URL+"/api/v3/context?context=cpu.cpu", &ctx)
	if ctx.API != 3 || ctx.ID != "cpu.cpu" || len(ctx.Context.Charts) != 2 {
		t.Fatalf("v3 context = %+v", ctx)
	}
	if resp := getJSON(t, ts.URL+"/api/v3/q?chart=system.ram&after=-5", nil); resp.StatusCode != 200 {
		t.Fatalf("v3 q status %d", resp.StatusCode)
	}
}

func TestDataAnomalyBit(t *testing.T) {
	src := fakeAnom{
		rate: map[string]float64{"system.ram|used": 100, "system.ram|free": 0},
		hist: map[string][]float64{"system.ram|used": {100}, "system.ram|free": {0}},
	}
	ts, reg := newTestServer(t, Options{Version: "test", Anomaly: src})
	now := time.Now().Truncate(time.Second)
	for i := 0; i < 5; i++ {
		_ = reg.Collect("system.ram", now.Add(time.Duration(i-4)*time.Second), map[string]float64{"used": 10, "free": 90})
	}
	var data struct {
		Units            string    `json:"units"`
		DimensionIDs     []string  `json:"dimension_ids"`
		DimensionAnomaly []float64 `json:"dimension_anomaly"`
		Result           struct {
			Data [][]*float64 `json:"data"`
		} `json:"result"`
	}
	getJSON(t, ts.URL+"/api/v1/data?chart=system.ram&after=-5&options=anomaly-bit", &data)
	if data.Units != "%" {
		t.Fatalf("units = %q", data.Units)
	}
	if len(data.DimensionAnomaly) != 2 || data.DimensionAnomaly[0] != 100 || data.DimensionAnomaly[1] != 0 {
		t.Fatalf("dimension_anomaly = %v ids=%v", data.DimensionAnomaly, data.DimensionIDs)
	}
	if len(data.Result.Data) == 0 {
		t.Fatal("no rows")
	}
	row := data.Result.Data[len(data.Result.Data)-1]
	if row[1] == nil || *row[1] != 100 {
		t.Fatalf("anomaly-bit last used = %v (ids=%v)", derefRow(row), data.DimensionIDs)
	}
}

func derefRow(row []*float64) []any {
	out := make([]any, len(row))
	for i, p := range row {
		if p == nil {
			out[i] = nil
			continue
		}
		out[i] = *p
	}
	return out
}

func TestAlertConfigCRUD(t *testing.T) {
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{ID: "id", Hostname: "test", UpdateEvery: 1}, db)
	reg.AddChart(&registry.Chart{ID: "system.ram", Context: "system.ram", Family: "ram", Units: "MiB",
		Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "free"}}})
	rules, err := health.ParseRules([]byte(`
alarms:
  - name: ram_in_use
    on: system.ram
    lookup: average -10s percentage of used
    units: '%'
    every: 1s
    warn: '$this > 80'
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	eng, err := health.New(reg, db, health.Options{Rules: rules, Hostname: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	sched := collect.NewScheduler(reg, nil, collect.Options{Names: []string{"none"}})
	srv, err := New(reg, db, sched, Options{Health: eng, StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	var listed struct {
		API     int `json:"api"`
		Count   int `json:"count"`
		Configs []struct {
			Name string `json:"name"`
			Hash string `json:"hash"`
		} `json:"configs"`
	}
	getJSON(t, ts.URL+"/api/v3/alert_config", &listed)
	if listed.API != 3 || listed.Count < 1 || listed.Configs[0].Name != "ram_in_use" || listed.Configs[0].Hash == "" {
		t.Fatalf("list = %+v", listed)
	}
	getJSON(t, ts.URL+"/api/v3/alert_config?hash="+listed.Configs[0].Hash, &struct {
		Config struct {
			Name string `json:"name"`
		} `json:"config"`
	}{})

	body := `{"name":"ram_hot","on":"system.ram","lookup":"average -5s of used","every":"1s","warn":"$this > 50"}`
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/v3/alert_config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(resp.Body)
		t.Fatalf("put status %d %s", resp.StatusCode, buf.String())
	}
	var put struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&put); err != nil {
		t.Fatal(err)
	}
	if put.Count != 1 {
		t.Fatalf("put count = %d", put.Count)
	}
	post, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v3/alert_config", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	post.Header.Set("Content-Type", "application/json")
	presp, err := http.DefaultClient.Do(post)
	if err != nil {
		t.Fatal(err)
	}
	presp.Body.Close()
	if presp.StatusCode != http.StatusConflict {
		t.Fatalf("post existing status %d", presp.StatusCode)
	}
	found := false
	for _, ru := range eng.Rules() {
		if ru.Spec.Name == "ram_hot" {
			found = true
		}
	}
	if !found {
		t.Fatal("ram_hot not in engine")
	}

	del, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/v3/alert_config?name=ram_hot", nil)
	if err != nil {
		t.Fatal(err)
	}
	dresp, err := http.DefaultClient.Do(del)
	if err != nil {
		t.Fatal(err)
	}
	dresp.Body.Close()
	if dresp.StatusCode != 200 {
		t.Fatalf("delete status %d", dresp.StatusCode)
	}
	for _, ru := range eng.Rules() {
		if ru.Spec.Name == "ram_hot" {
			t.Fatal("ram_hot still present")
		}
	}

	var tr struct {
		API int `json:"api"`
	}
	getJSON(t, ts.URL+"/api/v3/alert_transitions", &tr)
	if tr.API != 3 {
		t.Fatalf("v3 transitions api = %d", tr.API)
	}
}
