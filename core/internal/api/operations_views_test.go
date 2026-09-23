package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

const testViewsBody = `{"revision":0,"views":[{"name":"我的队列","query":"db","severity":"WARNING","nodeStatus":"live","pendingOnly":true,"ownerFilter":"mine","progressFilter":"watching"}]}`

func viewsTestServer(t *testing.T, opt Options) *Server {
	t.Helper()
	s, err := New(registry.New(&registry.Host{}, nil), nil, nil, opt)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func viewRequest(t *testing.T, s *Server, method, path, token, body string, want int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s got %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	return w
}

func readViews(t *testing.T, s *Server, token string) viewsResponse {
	t.Helper()
	w := viewRequest(t, s, "GET", "/api/v1/operations/views?node=ignored", token, "", 200)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cacheable personal response")
	}
	var v viewsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestPersonalViewsRBACIsolationAndRestart(t *testing.T) {
	opt := Options{Token: "admin-key", OperationsDir: t.TempDir(), Users: []User{
		{Name: "same-name", Token: "reader-key", Role: RoleViewer}, {Name: "same-name", Token: "other-key", Role: RoleTroubleshooter},
	}}
	s := viewsTestServer(t, opt)
	path := "/api/v1/operations/views"
	for _, token := range []string{"", "wrong"} {
		viewRequest(t, s, "GET", path, token, "", 401)
	}
	viewRequest(t, s, "POST", path, "reader-key", testViewsBody, 200)
	got := readViews(t, s, "reader-key")
	if !got.Enabled || !got.Persistent || got.Revision != 1 || len(got.Views) != 1 {
		t.Fatal(got)
	}
	for _, token := range []string{"admin-key", "other-key"} {
		if r := readViews(t, s, token); r.Revision != 0 || len(r.Views) != 0 {
			t.Fatal("cross-account leak", r)
		}
	}
	for _, path := range []string{"/api/v1/operations/handling", "/api/v1/operations/acknowledgements", "/api/v1/alarms/silence", "/api/v1/share"} {
		viewRequest(t, s, "POST", path, "reader-key", `{}`, 403)
	}
	viewRequest(t, s, "POST", path, "reader-key", testViewsBody, 409)
	for _, body := range []string{`{"views":[]}`, `{"revision":1,"views":null}`, `{"revision":1,"views":[],"principal":"forged"}`, `{"revision":1,"views":[]} {}`, strings.Repeat(" ", 32769) + `{}`} {
		viewRequest(t, s, "POST", path, "reader-key", body, 400)
	}
	s = viewsTestServer(t, opt)
	if r := readViews(t, s, "reader-key"); r.Revision != 1 || len(r.Views) != 1 {
		t.Fatal("restart lost views", r)
	}
	b, err := os.ReadFile(filepath.Join(opt.OperationsDir, "views.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"reader-key", "other-key", "admin-key", "same-name"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("identity/credential stored as plaintext")
		}
	}
	// Rotation starts a new credential space, rather than attaching by duplicate display name.
	opt.Users[0].Token = "rotated-key"
	s = viewsTestServer(t, opt)
	if r := readViews(t, s, "rotated-key"); len(r.Views) != 0 {
		t.Fatal("rotation inherited old credential space")
	}
}

func TestAnonymousAndShareCannotPersistPersonalViews(t *testing.T) {
	s := viewsTestServer(t, Options{})
	if r := readViews(t, s, ""); r.Enabled || len(r.Views) != 0 {
		t.Fatal(r)
	}
	viewRequest(t, s, "POST", "/api/v1/operations/views", "", testViewsBody, 403)
	s = viewsTestServer(t, Options{Token: "admin-key"})
	st := s.shares.issue("", time.Hour)
	if r := readViews(t, s, st.Token); r.Enabled || len(r.Views) != 0 {
		t.Fatal(r)
	}
	viewRequest(t, s, "POST", "/api/v1/operations/views", st.Token, testViewsBody, 403)
}

func TestLDAPPersonalViewsSurviveNewLogin(t *testing.T) {
	opt := Options{OperationsDir: t.TempDir(), LDAP: &LDAPConfig{URL: "ldaps://directory.example", UserDN: "uid=%s,dc=example", Bind: func(string, string) error { return nil }}}
	s := viewsTestServer(t, opt)
	login := func(user string) string {
		w := viewRequest(t, s, "POST", "/api/v1/auth/ldap", "", `{"user":"`+user+`","password":"test"}`, 200)
		var body struct{ Token string }
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Token
	}
	first := login("reader")
	viewRequest(t, s, "POST", "/api/v1/operations/views", first, testViewsBody, 200)
	viewRequest(t, s, "POST", "/api/v1/auth/oidc/logout", first, "", 204)
	viewRequest(t, s, "GET", "/api/v1/operations/views", first, "", 401)
	s = viewsTestServer(t, opt)
	if r := readViews(t, s, login("reader")); r.Revision != 1 {
		t.Fatal("new login lost views", r)
	}
	if r := readViews(t, s, login("other")); r.Revision != 0 {
		t.Fatal("LDAP cross-user leak", r)
	}
}

func TestViewPrincipalNamespaces(t *testing.T) {
	seen := map[string]bool{}
	for _, parts := range [][]string{{"static", "same"}, {"legacy", "same"}, {"oidc", "issuer", "same"}, {"oidc", "other-issuer", "same"}, {"oidc", "issuer", "other"}, {"ldap", "issuer", "same"}, {"oidc", "issuer:same", ""}} {
		p := viewPrincipal(parts...)
		if len(p) != 64 || seen[p] {
			t.Fatal("identity collision")
		}
		seen[p] = true
	}
}
