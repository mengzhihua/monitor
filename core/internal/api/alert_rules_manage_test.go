package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestAlertRulesWorkbenchAndLegacyAtomicity(t *testing.T) {
	dir := t.TempDir()
	base, err := health.Compile(health.RuleSpec{Name: "base", On: "system.ram", Calc: "$used", Warn: "$this > 10"}, "inline")
	if err != nil {
		t.Fatal(err)
	}
	e, err := health.New(registry.New(&registry.Host{Hostname: "test"}, nil), nil, health.Options{Rules: []*health.Rule{base}, LogDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ts, reg := newTestServer(t, Options{Health: e, Users: []User{{Name: "admin", Token: "admin", Role: RoleAdmin}, {Name: "viewer", Token: "viewer", Role: RoleViewer}, {Name: "ops", Token: "ops", Role: RoleTroubleshooter}}})
	call := func(method, path, token, body string, want int) []byte {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, resp.StatusCode, want, b)
		}
		if want == 200 && resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("cached rule response")
		}
		return b
	}
	read := func(path, token string) alertConfigResponse {
		t.Helper()
		var out alertConfigResponse
		if err := json.Unmarshal(call("GET", path, token, "", 200), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	mutate := func(action, revision string, spec *health.RuleSpec, name string, preview bool, want int) []byte {
		t.Helper()
		body, _ := json.Marshal(managedAlertRuleRequest{Action: action, Revision: revision, Config: spec, Name: name})
		path := "/api/v1/manage/alert-rules"
		if preview {
			path += "/preview"
		}
		return call("POST", path, "admin", string(body), want)
	}
	initial := read("/api/v1/alert_config", "admin")
	viewer := read("/api/v3/alert_config", "viewer")
	if initial.Scope != "local" || initial.Hostname != "test" || !initial.Persistent || !initial.CanManage || viewer.CanManage || viewer.Revision != initial.Revision || viewer.API != 3 || len(initial.Configs) != 1 || initial.Configs[0].Origin != "base" {
		t.Fatalf("metadata: %+v %+v", initial, viewer)
	}
	for i := 0; i < 55; i++ {
		reg.AddChart(&registry.Chart{ID: fmt.Sprintf("test.%02d", i), Context: "test.scope", Labels: map[string]string{"env": "prod"}})
	}
	reg.AddChart(&registry.Chart{ID: "test.skip", Context: "test.scope", Labels: map[string]string{"env": "dev"}})
	spec := health.RuleSpec{Name: "custom", On: "test.scope", Calc: "1", Warn: "$this > 0", Labels: map[string]string{"env": "prod"}, Disabled: true}
	var preview struct {
		Matched   int
		Charts    []alertRuleMatch
		Truncated bool
		Disabled  bool
		Revision  string
	}
	if err := json.Unmarshal(mutate("create", initial.Revision, &spec, "", true, 200), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Matched != 55 || len(preview.Charts) != 50 || !preview.Truncated || !preview.Disabled || preview.Revision != initial.Revision {
		t.Fatalf("scope: %+v", preview)
	}
	if !reflect.DeepEqual(initial, read("/api/v1/alert_config", "admin")) || len(e.Alarms()) != 0 {
		t.Fatal("preview changed runtime")
	}
	if _, err := os.Stat(filepath.Join(dir, "alert-rules.json")); !os.IsNotExist(err) {
		t.Fatal("preview wrote store")
	}
	invalid := spec
	invalid.Warn = "("
	mutate("create", initial.Revision, &invalid, "", true, 400)
	for _, role := range []string{"viewer", "ops"} {
		call("POST", "/api/v1/manage/alert-rules/preview", role, `{}`, 403)
		call("POST", "/api/v1/manage/alert-rules", role, `{}`, 403)
	}
	call("POST", "/api/v1/manage/alert-rules", "admin", `{"action":"delete","name":"base"}`, 400)
	call("GET", "/api/v1/alert_config?node=remote", "admin", "", 400)
	call("POST", "/api/v1/manage/alert-rules?node=remote", "admin", `{}`, 400)
	call("POST", "/api/v1/manage/alert-rules", "admin", strings.Repeat(" ", alertConfigMax+1), 413)
	mutate("create", initial.Revision, &spec, "", false, 200)
	created := read("/api/v1/alert_config", "admin")
	if created.Revision == initial.Revision || created.Count != 2 {
		t.Fatal("create did not publish revision")
	}
	mutate("update", initial.Revision, &spec, "", false, 409)
	mutate("update", created.Revision, &invalid, "", false, 400)
	if !reflect.DeepEqual(created, read("/api/v1/alert_config", "admin")) {
		t.Fatal("failed mutation changed state")
	}
	// Legacy bodies are decoded strictly and every batch is validated before applying.
	for _, body := range []string{
		`{"alarms":[{"name":"first","on":"system.ram","calc":"1"},{"name":"base","on":"system.ram","calc":"1"}]}`,
	} {
		call("POST", "/api/v3/alert_config", "admin", body, 409)
	}
	for _, body := range []string{
		`{"alarms":[{"name":"first","on":"system.ram","calc":"1"},{"name":"bad","on":"system.ram","calc":"("}]}`,
		`{"config":{"name":"x","on":"system.ram","calc":"1"},"alarms":[]}`,
		`{"config":{"name":"x","on":"system.ram","calc":"1","typo":true}}`,
		`{"name":"x","on":"system.ram","calc":"1"}{}`,
		"alarms:\n  - name: x\n    on: system.ram\n    calc: '1'\n---\nalarms: []\n",
	} {
		call("PUT", "/api/v1/alert_config", "admin", body, 400)
	}
	if !reflect.DeepEqual(created, read("/api/v1/alert_config", "admin")) {
		t.Fatal("legacy failed batch partially applied")
	}
	for i, body := range []string{
		`{"config":{"name":"wrapper","on":"system.ram","calc":"1"}}`,
		`{"alarms":[{"name":"envelope","on":"system.ram","calc":"1"}]}`,
		"alarms:\n  - name: yaml\n    on: system.ram\n    calc: '1'\n",
	} {
		var out alertConfigResponse
		if err := json.Unmarshal(call("PUT", "/api/v1/alert_config", "admin", body, 200), &out); err != nil || out.Count != 1 {
			t.Fatalf("legacy wrapper %d: %+v %v", i, out, err)
		}
	}
	rev := read("/api/v1/alert_config", "admin").Revision
	override := base.Spec
	override.Disabled = true
	mutate("update", rev, &override, "", false, 200)
	modified := read("/api/v1/alert_config", "admin")
	if modified.Configs[0].Origin != "override" || !modified.Configs[0].Config.Disabled {
		t.Fatal("missing override")
	}
	mutate("delete", modified.Revision, nil, "base", false, 200)
	deleted := read("/api/v1/alert_config", "admin")
	if !deleted.Configs[0].Deleted || !deleted.Configs[0].HasBase {
		t.Fatal("missing tombstone")
	}
	mutate("reset", deleted.Revision, nil, "base", true, 200)
	mutate("reset", deleted.Revision, nil, "base", false, 200)
	restored := read("/api/v1/alert_config", "admin")
	if restored.Configs[0].Origin != "base" || restored.Configs[0].Config.Disabled {
		t.Fatal("restore failed")
	}
	// An I/O failure must report unavailable and preserve the previous snapshot/file.
	file := filepath.Join(dir, "alert-rules.json")
	before, _ := os.ReadFile(file)
	if err := os.Rename(file, file+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file, 0700); err != nil {
		t.Fatal(err)
	}
	mutate("delete", restored.Revision, nil, "custom", false, 503)
	if !reflect.DeepEqual(restored, read("/api/v1/alert_config", "admin")) {
		t.Fatal("failed save published memory")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(file+".saved", file); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("failed save changed previous file")
	}
}

func TestAlertRulesManagedRequiresAuthenticatedAdmin(t *testing.T) {
	reg := registry.New(&registry.Host{Hostname: "test"}, nil)
	e, err := health.New(reg, nil, health.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	for _, opt := range []Options{{Health: e}, {}} {
		ts, _ := newTestServer(t, opt)
		resp, err := http.Post(ts.URL+"/api/v1/manage/alert-rules", "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if opt.Health != nil {
			for _, method := range []string{"POST", "PUT", "DELETE"} {
				request, _ := http.NewRequest(method, ts.URL+"/api/v3/alert_config?name=base", strings.NewReader(`{}`))
				denied, err := http.DefaultClient.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				denied.Body.Close()
				if denied.StatusCode != 403 {
					t.Fatalf("anonymous legacy %s: %d", method, denied.StatusCode)
				}
			}
		}
		want := 403
		if opt.Health == nil {
			want = 404
		}
		if resp.StatusCode != want {
			t.Fatalf("got %d want %d", resp.StatusCode, want)
		}
	}
}

// A runtime override invalid under the current overlay is not in
// snapshot.Rules; update/delete must still treat it as existing.
func TestAlertRulesManageInvalidOverride(t *testing.T) {
	dir := t.TempDir()
	base, err := health.Compile(health.RuleSpec{Name: "base", On: "system.ram", Calc: "$used", Warn: "$this > 10"}, "inline")
	if err != nil {
		t.Fatal(err)
	}
	e, err := health.New(registry.New(&registry.Host{Hostname: "test"}, nil), nil, health.Options{Rules: []*health.Rule{base}, LogDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err := e.SetOverlay(health.OverlayLayer{Rev: 1, Macros: map[string]string{"T": "50"}}); err != nil {
		t.Fatal(err)
	}
	rt := health.RuleSpec{Name: "rt.macro", On: "system.ram", Calc: "$used", Warn: "$this > {$T}", Every: "1s"}
	if _, err := e.MutateRules(nil, health.RuleMutation{Upserts: []health.RuleSpec{rt}}); err != nil {
		t.Fatal(err)
	}
	if err := e.SetOverlay(health.OverlayLayer{Rev: 2}); err != nil {
		t.Fatal(err)
	}
	if s := e.RulesConfig(); len(s.Invalid) != 1 || s.Invalid[0] != "rt.macro" {
		t.Fatalf("invalid=%v", s.Invalid)
	}

	ts, _ := newTestServer(t, Options{Health: e, Users: []User{{Name: "admin", Token: "admin", Role: RoleAdmin}}})
	call := func(action string, spec *health.RuleSpec, name string, want int) {
		t.Helper()
		body, _ := json.Marshal(managedAlertRuleRequest{Action: action, Revision: e.RulesConfig().Revision, Config: spec, Name: name})
		req, _ := http.NewRequest("POST", ts.URL+"/api/v1/manage/alert-rules", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer admin")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s %s: got %d want %d: %s", action, name, resp.StatusCode, want, b)
		}
	}

	// update with a now-valid config succeeds and clears the invalid entry
	fixed := health.RuleSpec{Name: "rt.macro", On: "system.ram", Calc: "$used", Warn: "$this > 90", Every: "1s"}
	call("update", &fixed, "", 200)
	if s := e.RulesConfig(); len(s.Invalid) != 0 {
		t.Fatalf("invalid after update=%v", s.Invalid)
	}
	// make it invalid again, then delete (restore the macro so the upsert
	// compiles, then remove it via the overlay)
	if err := e.SetOverlay(health.OverlayLayer{Rev: 3, Macros: map[string]string{"T": "50"}}); err != nil {
		t.Fatal(err)
	}
	call("update", &rt, "", 200)
	if err := e.SetOverlay(health.OverlayLayer{Rev: 4}); err != nil {
		t.Fatal(err)
	}
	if s := e.RulesConfig(); len(s.Invalid) != 1 {
		t.Fatalf("invalid=%v", s.Invalid)
	}
	call("delete", nil, "rt.macro", 200)
	if s := e.RulesConfig(); len(s.Invalid) != 0 {
		t.Fatalf("invalid after delete=%v", s.Invalid)
	}
	// create on an existing invalid name conflicts (it exists, in Invalid)
	call("update", &rt, "", 404) // rt.macro is gone now
	if err := e.SetOverlay(health.OverlayLayer{Rev: 5, Macros: map[string]string{"T": "50"}}); err != nil {
		t.Fatal(err)
	}
	rt3 := health.RuleSpec{Name: "rt.bad2", On: "system.ram", Calc: "$used", Warn: "$this > {$T}", Every: "1s"}
	call("create", &rt3, "", 200)
	if err := e.SetOverlay(health.OverlayLayer{Rev: 6}); err != nil {
		t.Fatal(err)
	}
	call("create", &rt3, "", 409) // exists in Invalid
}
