package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/hub"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

// newTemplatesServer wires a hub-mode Server with an Org + Templates store and
// three roles, no live nodes.
func newTemplatesServer(t *testing.T) (*httptest.Server, *hub.Templates) {
	t.Helper()
	reg := registry.New(&registry.Host{Hostname: "fixture", ID: "hub1"}, nil)
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	org, err := hub.OpenOrg(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tpls, err := hub.OpenTemplates(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sched := collect.NewScheduler(reg, nil, collect.Options{Names: []string{"none"}})
	s, err := New(reg, db, sched, Options{Mode: "hub", Org: org, Templates: tpls,
		OperationsDir: t.TempDir(), StartedAt: time.Now(), Users: []User{
			{Name: "reader", Token: "viewer", Role: RoleViewer},
			{Name: "ops", Token: "operator", Role: RoleTroubleshooter},
			{Name: "root", Token: "admin", Role: RoleAdmin}}})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, tpls
}

func tplReq(t *testing.T, method, url, token, body string) *http.Response {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, url, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestTemplatesAPI(t *testing.T) {
	ts, _ := newTemplatesServer(t)
	body := `{"name":"os-linux","macros":{"CPU_MAX":"90"},"rules":[{"name":"tpl.cpu","on":"system.cpu","calc":"$user","warn":"$this > {$CPU_MAX}","every":"10s"}],"assign":{"rooms":["rm1"]},"tags":{"env":"prod"}}`

	// viewer cannot write
	resp := tplReq(t, http.MethodPut, ts.URL+"/api/v1/hub/templates", "viewer", body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer PUT = %d, want 403", resp.StatusCode)
	}
	// troubleshooter cannot write either
	resp = tplReq(t, http.MethodPut, ts.URL+"/api/v1/hub/templates", "operator", body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("troubleshooter PUT = %d, want 403", resp.StatusCode)
	}

	resp = tplReq(t, http.MethodPut, ts.URL+"/api/v1/hub/templates", "admin", body)
	var saved struct {
		ID      string `json:"id"`
		Updated int64  `json:"updated"`
	}
	json.NewDecoder(resp.Body).Decode(&saved)
	resp.Body.Close()
	if resp.StatusCode != 200 || saved.ID == "" || saved.Updated == 0 {
		t.Fatalf("PUT = %d %+v", resp.StatusCode, saved)
	}

	// viewer can read
	resp = tplReq(t, http.MethodGet, ts.URL+"/api/v1/hub/templates", "viewer", "")
	if resp.StatusCode != 200 {
		t.Fatalf("GET = %d", resp.StatusCode)
	}
	var list struct {
		Templates []struct{ Name string } `json:"templates"`
	}
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list.Templates) != 1 || list.Templates[0].Name != "os-linux" {
		t.Fatalf("list: %+v", list)
	}

	// CAS conflict on stale if_updated
	resp = tplReq(t, http.MethodPut, ts.URL+"/api/v1/hub/templates?id="+saved.ID, "admin",
		`{"name":"os-linux-2","if_updated":999}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale PUT = %d, want 409", resp.StatusCode)
	}

	// invalid rules rejected
	resp = tplReq(t, http.MethodPut, ts.URL+"/api/v1/hub/templates", "admin",
		`{"name":"bad","rules":[{"name":"x","on":"c","calc":"$x","warn":"$this > {$MISSING}"}]}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad rules PUT = %d, want 400", resp.StatusCode)
	}

	// effective endpoint requires node
	resp = tplReq(t, http.MethodGet, ts.URL+"/api/v1/hub/templates/effective", "admin", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("effective without node = %d, want 400", resp.StatusCode)
	}
	// a node in no room/with no labels gets an empty overlay
	resp = tplReq(t, http.MethodGet, ts.URL+"/api/v1/hub/templates/effective?node=ghost", "admin", "")
	var ov struct {
		Rev       int64    `json:"rev"`
		Templates []string `json:"templates"`
	}
	json.NewDecoder(resp.Body).Decode(&ov)
	resp.Body.Close()
	if ov.Rev != 0 || len(ov.Templates) != 0 {
		t.Fatalf("effective: %+v", ov)
	}

	// inventory CAS
	resp = tplReq(t, http.MethodPut, ts.URL+"/api/v1/hub/inventory?node=n1", "admin",
		`{"fields":{"location":"dc1"}}`)
	if resp.StatusCode != 200 {
		t.Fatalf("inventory PUT = %d", resp.StatusCode)
	}
	var view struct {
		Manual map[string]string `json:"manual"`
	}
	json.NewDecoder(resp.Body).Decode(&view)
	resp.Body.Close()
	if view.Manual["location"] != "dc1" {
		t.Fatalf("inventory: %+v", view)
	}
	resp = tplReq(t, http.MethodGet, ts.URL+"/api/v1/hub/inventory?node=n1", "viewer", "")
	if resp.StatusCode != 200 {
		t.Fatalf("inventory GET = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// delete
	resp = tplReq(t, http.MethodDelete, ts.URL+"/api/v1/hub/templates?id="+saved.ID, "admin", "")
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("DELETE = %d", resp.StatusCode)
	}
}
