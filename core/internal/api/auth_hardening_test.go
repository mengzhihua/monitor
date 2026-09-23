package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOIDCOnlyRequiresAuthentication(t *testing.T) {
	ts, _ := newTestServer(t, Options{OIDC: &OIDCConfig{Issuer: "https://idp.example", ClientID: "monitor"}})
	for _, path := range []string{"/api/v1/info", "/metrics", "/api/v1/hub/spaces"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
	}
}
func TestOIDCSessionExpiry(t *testing.T) {
	o := newOIDC(&OIDCConfig{Issuer: "https://idp.example"})
	o.sessions["expired"] = oidcSession{User: User{Name: "viewer", Role: RoleViewer}, Expires: time.Now().Add(-time.Second)}
	if _, ok := o.session("expired"); ok {
		t.Fatal("expired session accepted")
	}
	if len(o.sessions) != 0 {
		t.Fatal("expired session retained")
	}
}
func TestOIDCCallbackRequiresBrowserState(t *testing.T) {
	s := &Server{oidc: newOIDC(&OIDCConfig{Issuer: "https://idp.example"})}
	s.oidc.pending["valid"] = oidcPending{Expires: time.Now().Add(time.Minute)}
	r := httptest.NewRequest("GET", "/api/v1/auth/oidc/callback?state=valid&code=code", nil)
	w := httptest.NewRecorder()
	s.handleOIDCCallback(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("callback without cookie: %d", w.Code)
	}
	if _, ok := s.oidc.pending["valid"]; !ok {
		t.Fatal("foreign browser consumed pending state")
	}
}

func TestLDAPOnlyAuthenticationAndLogout(t *testing.T) {
	ts, _ := newTestServer(t, Options{LDAP: &LDAPConfig{Bind: func(user, password string) error { return nil }, Role: "viewer"}})
	for _, path := range []string{"/api/v1/info", "/metrics", "/api/v1/hub/spaces"} {
		resp := getJSON(t, ts.URL+path, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anonymous %s: %d", path, resp.StatusCode)
		}
	}
	resp, err := http.Post(ts.URL+"/api/v1/auth/ldap", "application/json", strings.NewReader(`{"user":"alice","password":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	var login struct{ Token string }
	err = json.NewDecoder(resp.Body).Decode(&login)
	resp.Body.Close()
	if err != nil || login.Token == "" {
		t.Fatalf("login: %v", err)
	}
	if r := getJSON(t, ts.URL+"/api/v1/info?token="+login.Token, nil); r.StatusCode != 200 {
		t.Fatalf("authenticated read: %d", r.StatusCode)
	}
	for _, tc := range []struct {
		path string
		want int
	}{{"/api/v1/share", 403}, {"/api/v1/auth/oidc/logout", 204}} {
		req, _ := http.NewRequest("POST", ts.URL+tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+login.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Fatalf("%s: %d", tc.path, resp.StatusCode)
		}
	}
	if r := getJSON(t, ts.URL+"/api/v1/info?token="+login.Token, nil); r.StatusCode != 401 {
		t.Fatalf("revoked token: %d", r.StatusCode)
	}
}

func TestEmptyLDAPConfigLeavesLocalModeOpen(t *testing.T) {
	ts, _ := newTestServer(t, Options{LDAP: &LDAPConfig{}})
	if r := getJSON(t, ts.URL+"/api/v1/info", nil); r.StatusCode != 200 {
		t.Fatalf("empty LDAP: %d", r.StatusCode)
	}
}

func TestLDAPSRejectsUntrustedCertificate(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("credentials reached untrusted endpoint") }))
	defer ts.Close()
	err := ldapSimpleBind(&LDAPConfig{URL: strings.Replace(ts.URL, "https://", "ldaps://", 1)}, "alice", "secret")
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("expected TLS certificate rejection, got %v", err)
	}
}
