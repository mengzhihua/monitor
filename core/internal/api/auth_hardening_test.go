package api

import (
	"net/http"
	"net/http/httptest"
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
