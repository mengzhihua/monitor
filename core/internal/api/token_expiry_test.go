package api

import (
	"net/http"
	"testing"
	"time"
)

func TestNamedTokenExpiry(t *testing.T) {
	past := time.Now().Add(-time.Minute).Unix()
	future := time.Now().Add(time.Hour).Unix()
	ts, _ := newTestServer(t, Options{Users: []User{
		{Name: "old", Token: "old-token", Role: RoleViewer, ExpiresAt: past},
		{Name: "next", Token: "next-token", Role: RoleViewer, ExpiresAt: future},
		{Name: "keep", Token: "keep-token", Role: RoleViewer},
	}})
	status := func(token string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/info", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if status("old-token") != http.StatusUnauthorized {
		t.Fatalf("expired token status %d", status("old-token"))
	}
	if status("next-token") != http.StatusOK {
		t.Fatalf("future token status %d", status("next-token"))
	}
	if status("keep-token") != http.StatusOK {
		t.Fatalf("token without expiry status %d", status("keep-token"))
	}
	if err := validateUsers([]User{{Name: "bad", Token: "t", Role: RoleViewer, ExpiresAt: -1}}); err == nil {
		t.Fatal("accepted a negative expiry")
	}
	if err := validateUsers([]User{{Name: "bad", Token: "t", Role: RoleViewer, ExpiresAt: maxTokenExpiry + 1}}); err == nil {
		t.Fatal("accepted an expiry after 2100")
	}
}
