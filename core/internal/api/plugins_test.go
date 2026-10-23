package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	dsig "github.com/russellhaering/goxmldsig"

	"github.com/mengzhihua/monitor/core/internal/audit"
	"github.com/mengzhihua/monitor/core/internal/plugins"
)

func TestPluginSAMLOnlyAuthenticationAndRoles(t *testing.T) {
	keys := dsig.RandomKeyStoreForTest()
	_, certificate, err := keys.GetKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []Role{RoleAdmin, RoleTroubleshooter, RoleViewer} {
		t.Run(string(role), func(t *testing.T) {
			runtime := &pluginRuntimeFixture{}
			ts, _ := newTestServer(t, Options{Plugins: runtime, SAML: &SAMLConfig{
				EntityID: "monitor", SSOURL: "https://idp.example/sso", Role: string(role),
				CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate})),
			}})
			for _, token := range []string{"", "unknown-session"} {
				for _, path := range []string{"/api/v1/info", "/api/v1/platform", "/api/v1/plugins", "/metrics"} {
					if code, _ := pluginRequest(t, ts.URL+path, token, ""); code != http.StatusUnauthorized {
						t.Errorf("SAML-only unauthenticated %s: status %d, want 401", path, code)
					}
				}
			}
			if code, _ := pluginRequest(t, ts.URL+"/api/v1/plugins/actions", "", `{"action":"rescan","if_revision":7}`); code != http.StatusUnauthorized {
				t.Errorf("anonymous action: status %d, want 401", code)
			}
			assertion := signAssertion(t, keys, "_plugin-"+string(role), "operator@example.test")
			client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			response, err := client.PostForm(ts.URL+"/api/v1/auth/saml/acs", url.Values{
				"SAMLResponse": {base64.StdEncoding.EncodeToString(assertion)},
			})
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			location, err := response.Location()
			if response.StatusCode != http.StatusFound || err != nil {
				t.Fatal("signed assertion did not create a SAML session")
			}
			token := location.Query().Get("token")
			if token == "" {
				t.Fatal("SAML session credential missing")
			}
			code, body := pluginRequest(t, ts.URL+"/api/v1/info", token, "")
			var info struct {
				User     User         `json:"user"`
				Platform platformInfo `json:"platform"`
			}
			if code != http.StatusOK || json.Unmarshal([]byte(body), &info) != nil {
				t.Fatal("SAML session could not read server info")
			}
			if info.User.Name != "operator@example.test" || info.User.Role != role || !info.User.Personal {
				t.Errorf("authenticated SAML identity became %+v", info.User)
			}
			wantStatus, wantCapabilities := http.StatusForbidden, 0
			if role == RoleAdmin {
				wantStatus, wantCapabilities = http.StatusOK, 2
			}
			if len(info.Platform.Capabilities) != wantCapabilities {
				t.Errorf("role %s advertised capabilities %v", role, info.Platform.Capabilities)
			}
			code, body = pluginRequest(t, ts.URL+"/api/v1/platform", token, "")
			var platform platformInfo
			if code != http.StatusOK || json.Unmarshal([]byte(body), &platform) != nil || len(platform.Capabilities) != wantCapabilities || platform.Scope != info.Platform.Scope {
				t.Errorf("SAML role %s received inconsistent platform capabilities", role)
			}
			for _, request := range []struct{ path, body string }{
				{"/api/v1/plugins", ""}, {"/api/v1/plugins/actions", `{"action":"rescan","if_revision":7}`},
			} {
				if code, _ := pluginRequest(t, ts.URL+request.path, token, request.body); code != wantStatus {
					t.Errorf("SAML %s %s: status %d, want %d", role, request.path, code, wantStatus)
				}
			}
			runtime.mu.Lock()
			defer runtime.mu.Unlock()
			if runtime.calls != wantCapabilities/2 {
				t.Fatalf("SAML role %s reached plugin control %d times", role, runtime.calls)
			}
		})
	}
}

func TestPluginAndGenericAuthorizationDenialsAreAudited(t *testing.T) {
	log, err := audit.Open("", 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(log.Close)
	runtime := &pluginRuntimeFixture{}
	ts, _ := newTestServer(t, Options{Plugins: runtime, Audit: log, Users: []User{
		{Name: "reader", Token: "private-reader-credential", Role: RoleViewer},
		{Name: "operator", Token: "private-operator-credential", Role: RoleTroubleshooter},
		{Name: "restricted", Token: "private-room-credential", Role: RoleAdmin, Rooms: []string{"allowed"}},
		{Name: "admin", Token: "private-admin-credential", Role: RoleAdmin},
	}})
	for _, request := range []struct{ token, path, user string }{
		{"private-reader-credential", "/api/v1/plugins/actions", "reader"},
		{"private-operator-credential", "/api/v1/plugins/actions", "operator"},
		{"private-room-credential", "/api/v1/plugins/actions?node=forbidden", "restricted"},
		{"private-reader-credential", "/api/v1/alarms/silence", "reader"},
	} {
		before := len(log.Query(0, 0, ""))
		if code, _ := pluginRequest(t, ts.URL+request.path, request.token, `{"name":"example","action":"disable","if_revision":7}`); code != http.StatusForbidden {
			t.Fatalf("denied action returned %d", code)
		}
		entries := log.Query(0, 0, "")
		if len(entries) != before+1 {
			t.Errorf("denied %s by %s did not create one audit entry", request.path, request.user)
			continue
		}
		entry := entries[0]
		if entry.User != request.user || entry.Status != http.StatusForbidden || entry.Method != http.MethodPost || entry.Path != strings.Split(request.path, "?")[0] {
			t.Errorf("incorrect denied-action attribution: %+v", entry)
		}
	}
	if code, _ := pluginRequest(t, ts.URL+"/api/v1/plugins/actions", "private-admin-credential", `{"name":"example","action":"disable","if_revision":7}`); code != http.StatusOK {
		t.Fatalf("authorized action returned %d", code)
	}
	entry := log.Query(0, 1, "")[0]
	if entry.User != "admin" || entry.Status != http.StatusOK || entry.Target != "example" || entry.Note != "disable" {
		t.Errorf("authorized action lost its audit target or note: %+v", entry)
	}
	serialized, err := json.Marshal(log.Query(0, 0, ""))
	if err != nil || strings.Contains(string(serialized), "credential") {
		t.Fatal("audit contained request credentials")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.calls != 1 {
		t.Fatalf("denied requests reached plugin runtime: calls=%d", runtime.calls)
	}
}

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
