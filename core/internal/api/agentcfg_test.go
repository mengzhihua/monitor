package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestManageConfigRequiresAuthenticatedAdmin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	if err := os.WriteFile(path, []byte("global:\n  hostname: keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ts, _ := newTestServer(t, Options{
		ConfigPath: path,
		Users: []User{
			{Name: "Admin", Token: "admin", Role: RoleAdmin},
			{Name: "Reader", Token: "viewer", Role: RoleViewer},
			{Name: "Oncall", Token: "operator", Role: RoleTroubleshooter},
		},
	})
	for _, c := range []struct {
		method, token string
		want          int
	}{
		{http.MethodGet, "", 401},
		{http.MethodGet, "viewer", 403},
		{http.MethodPut, "viewer", 403},
		{http.MethodPut, "operator", 403},
		{http.MethodPost, "admin", 404},
	} {
		req, _ := http.NewRequest(c.method, ts.URL+"/api/v1/manage/config", strings.NewReader(`{"yaml":"global:\n  hostname: x\n"}`))
		if c.method == http.MethodPost {
			req, _ = http.NewRequest(c.method, ts.URL+"/api/v1/manage/restart", nil)
		}
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Fatalf("%s %s: status %d, want %d", c.method, c.token, resp.StatusCode, c.want)
		}
	}
	open, _ := newTestServer(t, Options{ConfigPath: path})
	req, _ := http.NewRequest(http.MethodPut, open.URL+"/api/v1/manage/config", strings.NewReader("global:\n  hostname: anon\n"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("anonymous status %d", resp.StatusCode)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "global:\n  hostname: keep\n" {
		t.Fatalf("file changed: %q", got)
	}
}

func TestManageConfigRoundTripAndInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	original := "global:\n  hostname: before\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	ts, _ := newTestServer(t, Options{ConfigPath: path, Token: "admin-token"})
	var out agentConfigResponse
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/manage/config", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("get %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	if !out.Writable || out.Path != path || out.YAML != original {
		t.Fatalf("get = %+v", out)
	}

	bad, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/manage/config", strings.NewReader(`{"yaml":"web: ["}`))
	bad.Header.Set("Authorization", "Bearer admin-token")
	resp, err = http.DefaultClient.Do(bad)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid status %d", resp.StatusCode)
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatalf("invalid write changed file: %q", got)
	}

	next := "global:\n  hostname: after\n"
	ok, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/manage/config", strings.NewReader(`{"yaml":"global:\n  hostname: after\n"}`))
	ok.Header.Set("Authorization", "Bearer admin-token")
	resp, err = http.DefaultClient.Do(ok)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("put %d", resp.StatusCode)
	}
	got, _ = os.ReadFile(path)
	if string(got) != next {
		t.Fatalf("file = %q", got)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %o", st.Mode().Perm())
		}
	}
}

func TestManageRestartCallsHook(t *testing.T) {
	called := make(chan struct{}, 1)
	ts, _ := newTestServer(t, Options{
		Token: "admin-token",
		RequestRestart: func() error {
			called <- struct{}{}
			return nil
		},
	})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/manage/restart", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status %d", resp.StatusCode)
	}
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("restart hook was not called")
	}
	missing, _ := newTestServer(t, Options{Token: "admin-token"})
	req, _ = http.NewRequest(http.MethodPost, missing.URL+"/api/v1/manage/restart", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("nil hook status %d", resp.StatusCode)
	}
}

func TestManageConfigFormKeepsUntouchedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	original := "global:\n  hostname: old\nweb:\n  token: secret-token\ncollectors:\n  modules:\n    nginx:\n      url: http://old/stub_status\n      timeout: 2s\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	ts, _ := newTestServer(t, Options{ConfigPath: path, Token: "admin-token"})
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/manage/config", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out agentConfigResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	if out.Form == nil || out.Form.Hostname != "old" || out.FormError != "" {
		t.Fatalf("form = %+v err %q", out.Form, out.FormError)
	}
	out.Form.Hostname = "new-host"
	for i := range out.Form.Targets {
		if out.Form.Targets[i].Name == "nginx" {
			out.Form.Targets[i].URL = "http://127.0.0.1/stub_status"
		}
	}
	body, _ := json.Marshal(map[string]any{"form": out.Form, "if_updated": out.Updated})
	put, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/manage/config", strings.NewReader(string(body)))
	put.Header.Set("Authorization", "Bearer admin-token")
	resp, err = http.DefaultClient.Do(put)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("form put %d", resp.StatusCode)
	}
	got, _ := os.ReadFile(path)
	text := string(got)
	for _, keep := range []string{"secret-token", "timeout: 2s", "new-host", "http://127.0.0.1/stub_status"} {
		if !strings.Contains(text, keep) {
			t.Fatalf("missing %s\n%s", keep, text)
		}
	}
	both, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/manage/config", strings.NewReader(`{"yaml":"global:\n  hostname: x\n","form":{"mode":"agent","update_every":1,"web_enabled":"default","health_enabled":"default"}}`))
	both.Header.Set("Authorization", "Bearer admin-token")
	resp, err = http.DefaultClient.Do(both)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("both status %d", resp.StatusCode)
	}
}

func TestManageConfigRollbackAndRestartGuard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	original := "global:\n  hostname: before\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	called := make(chan struct{}, 1)
	ts, _ := newTestServer(t, Options{
		ConfigPath: path,
		Token:      "admin-token",
		RequestRestart: func() error {
			called <- struct{}{}
			return nil
		},
	})
	authReq := func(method, url, body string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer admin-token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	missing := authReq(http.MethodPost, ts.URL+"/api/v1/manage/config/rollback", "")
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing backup %d", missing.StatusCode)
	}
	put := authReq(http.MethodPut, ts.URL+"/api/v1/manage/config", `{"yaml":"global:\n  hostname: after\n"}`)
	put.Body.Close()
	if put.StatusCode != http.StatusOK {
		t.Fatalf("put %d", put.StatusCode)
	}
	back := authReq(http.MethodPost, ts.URL+"/api/v1/manage/config/rollback", "")
	back.Body.Close()
	if back.StatusCode != http.StatusOK {
		t.Fatalf("rollback %d", back.StatusCode)
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatalf("restored %q", got)
	}
	if err := os.WriteFile(path, []byte("web: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp := authReq(http.MethodPost, ts.URL+"/api/v1/manage/restart", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("restart guard %d", resp.StatusCode)
	}
	select {
	case <-called:
		t.Fatal("restart hook ran for a file that will not load")
	case <-time.After(300 * time.Millisecond):
	}
}
