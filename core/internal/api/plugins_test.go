package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/mengzhihua/monitor/core/internal/plugins"
)

type pluginRuntimeFixture struct {
	mu    sync.Mutex
	calls int
	last  plugins.ControlRequest
	err   error
}

func (p *pluginRuntimeFixture) Status() []plugins.Status { return []plugins.Status{} }
func (p *pluginRuntimeFixture) Catalog() plugins.Catalog {
	return plugins.Catalog{SchemaVersion: 1, Revision: 7, Running: true, Plugins: []plugins.Status{}, Diagnostics: []plugins.Diagnostic{}}
}
func (p *pluginRuntimeFixture) Apply(_ context.Context, request plugins.ControlRequest) (plugins.Catalog, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.last = request
	return p.Catalog(), p.err
}

func pluginRequest(t *testing.T, url, token, body string) (int, string) {
	t.Helper()
	method := http.MethodGet
	if body != "" {
		method = http.MethodPost
	}
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

func TestPluginPlatformAuthorizationAndLocalScope(t *testing.T) {
	runtime := &pluginRuntimeFixture{}
	ts, _ := newTestServer(t, Options{Plugins: runtime, Token: "admin-fixture", Users: []User{{Name: "reader", Token: "reader-fixture", Role: RoleViewer}}})
	for _, tt := range []struct {
		token, path, body string
		code              int
	}{
		{"", "/api/v1/plugins", "", 401},
		{"reader-fixture", "/api/v1/plugins", "", 403},
		{"reader-fixture", "/api/v1/plugins/actions", `{"action":"rescan","if_revision":7}`, 403},
		{"admin-fixture", "/api/v1/plugins?node=remote", "", 400},
		{"admin-fixture", "/api/v1/plugins/actions?node=remote", `{"action":"rescan","if_revision":7}`, 400},
		{"admin-fixture", "/api/v1/plugins", "", 200},
		{"admin-fixture", "/api/v1/plugins/actions", `{"action":"rescan","if_revision":7}`, 200},
	} {
		if code, _ := pluginRequest(t, ts.URL+tt.path, tt.token, tt.body); code != tt.code {
			t.Errorf("%s: status %d want %d", tt.path, code, tt.code)
		}
	}
	for token, want := range map[string]int{"admin-fixture": 2, "reader-fixture": 0} {
		code, body := pluginRequest(t, ts.URL+"/api/v1/platform", token, "")
		var platform platformInfo
		if code != 200 || json.Unmarshal([]byte(body), &platform) != nil || len(platform.Capabilities) != want || platform.Scope != "local" {
			t.Fatalf("platform capability mismatch for authenticated role: %d %s", code, body)
		}
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.calls != 1 || runtime.last.Action != "rescan" {
		t.Fatalf("unauthorized operations reached runtime: %d", runtime.calls)
	}
}

func TestPluginPlatformAnonymousAndDisabled(t *testing.T) {
	for _, opt := range []Options{{Plugins: &pluginRuntimeFixture{}}, {Token: "admin-fixture"}} {
		ts, _ := newTestServer(t, opt)
		code, body := pluginRequest(t, ts.URL+"/api/v1/platform", opt.Token, "")
		var platform platformInfo
		if code != 200 || json.Unmarshal([]byte(body), &platform) != nil || len(platform.Capabilities) != 0 {
			t.Fatal("unavailable runtime advertised controls")
		}
		code, _ = pluginRequest(t, ts.URL+"/api/v1/plugins", opt.Token, "")
		if code != 403 && code != 503 {
			t.Fatalf("anonymous or disabled runtime accepted request: %d", code)
		}
	}
}

func TestPluginActionsRejectMalformedAndMapConflicts(t *testing.T) {
	runtime := &pluginRuntimeFixture{}
	ts, _ := newTestServer(t, Options{Plugins: runtime, Token: "admin-fixture"})
	for _, body := range []string{`{}`, `{"action":"rescan"}`, `{"action":"rescan","if_revision":7,"command":"/bin/sh"}`, `{"action":"rescan","if_revision":7} {}`, strings.Repeat(" ", 4097) + `{}`} {
		if code, _ := pluginRequest(t, ts.URL+"/api/v1/plugins/actions", "admin-fixture", body); code != 400 {
			t.Fatalf("malformed request: %d", code)
		}
	}
	for _, tt := range []struct {
		err    error
		status int
	}{{plugins.ErrConflict, 409}, {plugins.ErrNotFound, 404}, {plugins.ErrPolicy, 403}, {plugins.ErrInvalid, 400}, {plugins.ErrUnavailable, 503}, {context.DeadlineExceeded, 503}} {
		runtime.mu.Lock()
		runtime.err = tt.err
		runtime.mu.Unlock()
		if code, _ := pluginRequest(t, ts.URL+"/api/v1/plugins/actions", "admin-fixture", `{"name":"example","action":"restart","if_revision":7}`); code != tt.status {
			t.Fatalf("error %v: %d", tt.err, code)
		}
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.calls != 6 {
		t.Fatalf("malformed actions reached runtime: %d", runtime.calls)
	}
}
