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
