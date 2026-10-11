package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/hub"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func TestAlarmVariableAPIExcludesMissingLatestDimensions(t *testing.T) {
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{ID: "hub", UpdateEvery: 1}, db)
	chart := &registry.Chart{ID: "sample", Dimensions: []*registry.Dimension{{ID: "used", Name: "used_alias"}, {ID: "free"}}}
	reg.AddChart(chart)
	now := time.Now().Truncate(time.Second)
	for i, values := range []map[string]float64{{"used": 90, "free": 10}, {"free": 30}} {
		if err := reg.Collect("sample", now.Add(time.Duration(i)*time.Second), values); err != nil {
			t.Fatal(err)
		}
	}
	engine, err := health.New(reg, db, health.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Close)
	nodes, err := hub.Open(db, t.TempDir(), hub.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nodes.AcceptReplica(registry.Host{ID: "remote", UpdateEvery: 1}, []*registry.Chart{chart}, []hub.ReplicaSample{
		{Chart: "sample", T: now.Unix(), V: map[string]float64{"used": 90, "free": 10}},
		{Chart: "sample", T: now.Unix() + 1, V: map[string]float64{"free": 30}},
	}, now); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, node string
		health     *health.Engine
	}{{"local with engine", "", engine}, {"local without engine", "", nil}, {"remote", "remote", engine}} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := New(reg, db, nil, Options{Health: tc.health, Nodes: nodes})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodGet, "/api/v1/alarm_variables?chart=sample&node="+tc.node, nil)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			var body struct {
				Variables map[string]float64 `json:"variables"`
			}
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil {
				t.Fatalf("invalid variable response: %d %s", w.Code, w.Body.String())
			}
			if body.Variables["free"] != 30 {
				t.Fatal("missing fresh dimension", body.Variables)
			}
			for _, name := range []string{"used", "used_alias"} {
				if _, ok := body.Variables[name]; ok {
					t.Fatal("stale dimension exposed", name)
				}
			}
		})
	}
}
