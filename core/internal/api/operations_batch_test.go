package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/operations"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func TestHandlingBatchRBACAtomicityAndEpisodeChanges(t *testing.T) {
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{ID: "local", Hostname: "batch-host", UpdateEvery: 1}, db)
	reg.AddChart(&registry.Chart{ID: "system.ram", Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "free"}}})
	rules, err := health.ParseRules([]byte(`alarms:
  - name: first
    on: system.ram
    calc: '$used'
    every: 1s
    warn: '$this > 50'
    crit: '$this > 90'
  - name: second
    on: system.ram
    calc: '$used'
    every: 1s
    warn: '$this > 50'
`), "batch-test")
	if err != nil {
		t.Fatal(err)
	}
	eng, err := health.New(reg, db, health.Options{Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	dir := t.TempDir()
	s, err := New(reg, db, collect.NewScheduler(reg, nil, collect.Options{Names: []string{"none"}}), Options{Health: eng, OperationsDir: dir, Token: "admin", Users: []User{{Name: "on-call", Role: RoleTroubleshooter, Token: "operator"}, {Name: "reader", Role: RoleViewer, Token: "viewer"}}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	set := func(value float64, sec int) {
		_ = reg.Collect("system.ram", now.Add(time.Duration(sec)*time.Second), map[string]float64{"used": value, "free": 100 - value})
		eng.Tick(now.Add(time.Duration(sec) * time.Second))
	}
	set(80, 0)
	snapshot := func() operationsSnapshot {
		return s.operationsSnapshot(httptest.NewRequest("GET", "/api/v1/operations", nil))
	}
	initial := snapshot().Problems
	if len(initial) != 2 {
		t.Fatal(initial)
	}
	items := []map[string]any{{"id": initial[0].ID, "revision": 0}, {"id": initial[1].ID, "revision": 0}}
	const path = "/api/v1/operations/handling/batch"
	post := func(token string, body map[string]any, want int) *httptest.ResponseRecorder {
		t.Helper()
		b, _ := json.Marshal(body)
		return viewRequest(t, s, "POST", path, token, string(b), want)
	}
	body := map[string]any{"action": "acknowledge", "items": items, "note": "reviewed together"}
	post("", body, 401)
	post("viewer", body, 403)
	for _, invalid := range []map[string]any{
		{"action": "acknowledge", "items": []any{}},
		{"action": "acknowledge", "items": []any{items[0], items[0]}},
		{"action": "acknowledge", "items": []any{map[string]any{"id": initial[0].ID}}},
		{"action": "acknowledge", "items": items, "actor": "forged"},
		{"action": "assign", "items": items, "assignee": "reader"},
		{"action": "assign", "items": items, "assignee": "missing"},
		{"action": "progress", "items": items, "status": "resolved"},
		{"action": "comment", "items": items, "note": " "},
	} {
		post("operator", invalid, 400)
	}
	tooMany := make([]map[string]any, 51)
	for i := range tooMany {
		tooMany[i] = items[0]
	}
	post("operator", map[string]any{"action": "acknowledge", "items": tooMany}, 400)
	viewRequest(t, s, "POST", path, "operator", strings.Repeat(" ", 16385)+`{}`, 400)
	post("operator", map[string]any{"action": "acknowledge", "items": []map[string]any{items[0], {"id": strings.Repeat("0", 64), "revision": 0}}}, 409)
	for _, p := range initial {
		if s.operations.Get(p.ID).Revision != 0 {
			t.Fatal("invalid request partially saved")
		}
	}
	w := post("operator", body, 200)
	var response struct {
		Count   int
		Records []operations.Record
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Count != 2 {
		t.Fatal(w.Body.String(), err)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cacheable response")
	}
	for _, r := range response.Records {
		if !r.Acknowledged || r.History[0].Actor != "on-call" || r.History[0].Note != "reviewed together" {
			t.Fatal(r)
		}
	}
	for _, item := range items {
		item["revision"] = 1
	}
	post("admin", map[string]any{"action": "assign", "items": items, "assignee": "on-call"}, 200)
	for _, item := range items {
		item["revision"] = 2
	}
	// A single-record edit invalidates the whole subsequent batch.
	if _, err := s.operations.Apply(initial[1].ID, "other", operations.Change{Action: "comment", Note: "racing update"}, 2, operations.Target{Name: initial[1].Name}); err != nil {
		t.Fatal(err)
	}
	before := s.operations.Recent()
	post("operator", map[string]any{"action": "progress", "items": items, "status": "watching"}, 409)
	if !reflect.DeepEqual(before, s.operations.Recent()) {
		t.Fatal("conflict partially saved")
	}
	items[1]["revision"] = 3
	// Escalating only one rule leaves the other active; the old selected ID still rejects all.
	set(95, 2)
	post("operator", map[string]any{"action": "progress", "items": items, "status": "watching"}, 409)
	if !reflect.DeepEqual(before, s.operations.Recent()) {
		t.Fatal("changed episode partially saved")
	}
	current := snapshot().Problems
	items = nil
	for _, p := range current {
		items = append(items, map[string]any{"id": p.ID, "revision": p.Handling.Revision})
	}
	pathOnDisk := filepath.Join(dir, "acknowledgements.json")
	if err := os.Remove(pathOnDisk); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(pathOnDisk, 0700); err != nil {
		t.Fatal(err)
	}
	post("operator", map[string]any{"action": "progress", "items": items, "status": "watching"}, 503)
	if !reflect.DeepEqual(before, s.operations.Recent()) {
		t.Fatal("storage failure partially saved")
	}
	if err := os.Remove(pathOnDisk); err != nil {
		t.Fatal(err)
	}
	post("operator", map[string]any{"action": "progress", "items": items, "status": "watching"}, 200)
	for _, a := range eng.Alarms() {
		if (a.Status != health.StatusWarning && a.Status != health.StatusCritical) || eng.IsSilenced(a.Chart, a.Name) {
			t.Fatal("handling changed health evaluation")
		}
	}
}
