package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestMaintenanceAPIValidationRBACAndWriteFailure(t *testing.T) {
	dir := t.TempDir()
	reg := registry.New(&registry.Host{Hostname: "local"}, nil)
	reg.AddChart(&registry.Chart{ID: "system.ram", Dimensions: []*registry.Dimension{{ID: "used"}}})
	rules, err := health.ParseRules([]byte("alarms:\n  - name: ram_test\n    on: system.ram\n    calc: '$used'\n    warn: '$this > 50'\n"), "test")
	if err != nil {
		t.Fatal(err)
	}
	e, err := health.New(reg, nil, health.Options{Rules: rules, LogDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.Tick(time.Now()) // bind a real local rule; missing data must not fabricate an alert
	ts, _ := newTestServer(t, Options{Health: e, Users: []User{
		{Name: "Admin", Token: "admin", Role: RoleAdmin}, {Name: "Reader", Token: "viewer", Role: RoleViewer}, {Name: "Oncall", Token: "operator", Role: RoleTroubleshooter},
	}})
	path := "/api/v1/operations/maintenance"
	call := func(token, query, body string, want int) maintenanceResponse {
		t.Helper()
		method := "GET"
		if body != "" {
			method = "POST"
		}
		req, _ := http.NewRequest(method, ts.URL+path+query, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("want %d, got %d: %s", want, resp.StatusCode, b)
		}
		var out maintenanceResponse
		if want == 200 {
			if resp.Header.Get("Cache-Control") != "no-store" {
				t.Fatal("cached")
			}
			if err := json.Unmarshal(b, &out); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	call("", "", "", 401)
	reader := call("viewer", "", "", 200)
	if !reader.Available || reader.CanManage || !reader.Persistent || len(reader.Targets) != 1 || reader.Plans == nil {
		t.Fatal(reader)
	}
	body := `{"action":"create","revision":0,"plan":{"title":"Upgrade","reason":"hardware change","scope":"alarm","chart":"system.ram","alarm":"ram_test","duration_seconds":3600}}`
	call("viewer", "", body, 403)
	call("operator", "", body, 403)
	call("admin", "?node=remote-agent", body, 400)
	for _, invalid := range []string{
		strings.Replace(body, `"revision":0,`, "", 1),
		strings.Replace(body, `"revision":0`, `"revision":null`, 1),
		strings.Replace(body, `"action":"create"`, `"actor":"forged","action":"create"`, 1),
		strings.Replace(body, `"alarm":"ram_test"`, `"alarm":"missing"`, 1),
		strings.Replace(body, `"scope":"alarm"`, `"scope":"all"`, 1),
		body + `{}`, strings.Replace(body, "hardware change", strings.Repeat("x", 9000), 1),
	} {
		call("admin", "", invalid, 400)
	}
	created := call("admin", "", body, 200)
	if !created.CanManage || created.Revision != 1 || created.Plans[0].CreatedBy != "Admin" || created.Plans[0].State != "active" || !e.IsSilenced("system.ram", "ram_test") {
		t.Fatal(created)
	}
	call("admin", "", body, 409)
	id := created.Plans[0].ID
	cancel := `{"action":"cancel","revision":1,"id":"` + id + `","reason":"finished"}`
	file := filepath.Join(dir, "maintenance-plans.json")
	if err := os.Rename(file, file+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file, 0700); err != nil {
		t.Fatal(err)
	}
	call("admin", "", cancel, 503)
	now := call("admin", "", "", 200)
	if !reflect.DeepEqual(created.MaintenancePlanSnapshot, now.MaintenancePlanSnapshot) || !e.IsSilenced("system.ram", "ram_test") {
		t.Fatal("failed save changed suppression")
	}
	_ = os.Remove(file)
	_ = os.Rename(file+".saved", file)
	ended := call("admin", "", cancel, 200)
	if ended.Plans[0].State != "canceled" || ended.Plans[0].CanceledBy != "Admin" || e.IsSilenced("system.ram", "ram_test") {
		t.Fatal(ended)
	}
	content, _ := os.ReadFile(file)
	if bytes.Contains(content, []byte(`"token"`)) {
		t.Fatal("stored credentials")
	}
}

func TestMaintenanceAPIDisabledAndAnonymous(t *testing.T) {
	ts, _ := newTestServer(t, Options{Token: "admin"})
	var got maintenanceResponse
	getJSON(t, ts.URL+"/api/v1/operations/maintenance?token=admin", &got)
	if got.Available || got.CanManage || got.Plans == nil {
		t.Fatal(got)
	}
	e, _ := health.New(nil, nil, health.Options{})
	defer e.Close()
	ts2, _ := newTestServer(t, Options{Health: e})
	getJSON(t, ts2.URL+"/api/v1/operations/maintenance", &got)
	if !got.Available || got.CanManage {
		t.Fatal("anonymous management enabled")
	}
	resp, err := http.Post(ts2.URL+"/api/v1/operations/maintenance", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal(resp.StatusCode)
	}
}
