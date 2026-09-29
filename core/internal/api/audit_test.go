package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/audit"
	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func newCloseTestServer(t *testing.T) (*httptest.Server, *health.Engine, *audit.Log) {
	t.Helper()
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{ID: "id", Hostname: "test", OS: "linux", UpdateEvery: 1}, db)
	reg.AddChart(&registry.Chart{ID: "system.ram", Context: "system.ram", Family: "ram", Units: "MiB",
		Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "free"}}})
	rules, err := health.ParseRules([]byte(`
alarms:
  - name: ram_high
    on: system.ram
    calc: '$used'
    every: 1s
    warn: '$this > 50'
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	eng, err := health.New(reg, db, health.Options{Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	al, err := audit.Open(t.TempDir(), 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(al.Close)
	sched := collect.NewScheduler(reg, nil, collect.Options{Names: []string{"none"}})
	opt := Options{Health: eng, Audit: al, OperationsDir: t.TempDir(), Users: []User{
		{Name: "reader", Token: "viewer", Role: RoleViewer},
		{Name: "ops", Token: "operator", Role: RoleTroubleshooter},
		{Name: "root", Token: "admin", Role: RoleAdmin}}}
	s, err := New(reg, db, sched, opt)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	now := time.Now().Truncate(time.Second)
	_ = reg.Collect("system.ram", now, map[string]float64{"used": 80, "free": 20})
	eng.Tick(now)
	return ts, eng, al
}

func postClose(t *testing.T, url, token, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAlarmCloseAPI(t *testing.T) {
	ts, eng, al := newCloseTestServer(t)
	alarmID := eng.Alarms()[0].ID

	// viewer cannot close.
	resp := postClose(t, ts.URL+"/api/v1/alarms/close", "viewer", `{"alarm_id":1}`)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("viewer close: %d", resp.StatusCode)
	}

	// unknown id → 404 for admin.
	resp = postClose(t, ts.URL+"/api/v1/alarms/close", "admin", `{"alarm_id":999999}`)
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("unknown id: %d", resp.StatusCode)
	}

	// troubleshooter closes the raised alarm.
	resp = postClose(t, ts.URL+"/api/v1/alarms/close", "operator", `{"alarm_id":`+jsonNumber(alarmID)+`,"comment":"ok"}`)
	var a health.Alarm
	if resp.StatusCode != 200 {
		t.Fatalf("close: %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if a.Status != health.StatusClear {
		t.Fatalf("status %s want CLEAR", a.Status)
	}

	// The mutation was audited with user/role/status and the alarm id target.
	entries := al.Query(0, 0, "")
	var found bool
	for _, e := range entries {
		if e.Action == "POST /api/v1/alarms/close" && e.User == "ops" && e.Role == "troubleshooter" && e.Status == 200 {
			if e.Target != jsonNumber(alarmID) {
				t.Fatalf("audit target = %q, want %d", e.Target, alarmID)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("close not audited: %+v", entries)
	}

	// A node= that resolves to a remote/unknown hub node is refused; an
	// unknown node fails node resolution (404) before the 501.
	resp = postClose(t, ts.URL+"/api/v1/alarms/close?node=abc123", "admin", `{"alarm_id":1}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("remote close: %d", resp.StatusCode)
	}
}

func jsonNumber(v uint64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestAuditMiddlewareSkipsReadsAndIngest(t *testing.T) {
	ts, _, al := newCloseTestServer(t)

	// GET requests are not audited.
	getJSON(t, ts.URL+"/api/v1/alarms?token=admin", nil)
	// An excluded ingest path is not audited.
	resp := postClose(t, ts.URL+"/api/v1/checks", "operator", `{"name":"x","status":1}`)
	resp.Body.Close()
	// A mutating request is.
	resp = postClose(t, ts.URL+"/api/v1/alarms/silence?all=true", "admin", `{}`)
	resp.Body.Close()

	var gotPost, gotGet bool
	for _, e := range al.Query(0, 0, "") {
		if e.Path == "/api/v1/alarms" {
			gotGet = true
		}
		if e.Path == "/api/v1/checks" {
			t.Fatalf("checks should be excluded: %+v", e)
		}
		if e.Action == "POST /api/v1/alarms/silence" && e.User == "root" {
			gotPost = true
		}
	}
	if gotGet {
		t.Fatal("GET was audited")
	}
	if !gotPost {
		t.Fatal("POST silence was not audited")
	}

	// /api/v1/audit is admin-only.
	if resp := getJSON(t, ts.URL+"/api/v1/audit?token=viewer", nil); resp.StatusCode != 403 {
		t.Fatalf("viewer audit read: %d", resp.StatusCode)
	}
	var out struct {
		Entries []audit.Entry `json:"entries"`
	}
	getJSON(t, ts.URL+"/api/v1/audit?token=admin", &out)
	if len(out.Entries) == 0 {
		t.Fatal("audit endpoint returned nothing")
	}
}

func TestAuditWrapperPerRequest(t *testing.T) {
	// Two sequential POSTs by different users must produce exactly one entry
	// each, attributed to the right user (no wrapper accumulation).
	ts, _, al := newCloseTestServer(t)
	for _, tok := range []string{"operator", "admin", "operator"} {
		resp := postClose(t, ts.URL+"/api/v1/alarms/close", tok, `{"alarm_id":999999}`)
		resp.Body.Close()
	}
	var ops, root int
	for _, e := range al.Query(0, 0, "") {
		if e.Action != "POST /api/v1/alarms/close" {
			continue
		}
		switch e.User {
		case "ops":
			ops++
		case "root":
			root++
		}
	}
	if ops != 2 || root != 1 {
		t.Fatalf("want ops=2 root=1, got ops=%d root=%d: %+v", ops, root, al.Query(0, 0, ""))
	}
}

func TestAuditLoginFailed(t *testing.T) {
	ts, _, al := newCloseTestServer(t)
	resp, err := http.Post(ts.URL+"/api/v1/alarms/silence", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
	var found bool
	for _, e := range al.Query(0, 0, "") {
		if e.Action == "login_failed" {
			found = true
		}
	}
	if !found {
		t.Fatal("401 not audited as login_failed")
	}
}
