package api

import (
	"io"
	"net/http"
	"testing"
)

func TestDebugHeapRequiresAdmin(t *testing.T) {
	ts, _ := newTestServer(t, Options{
		Users: []User{
			{Name: "reader", Token: "viewer", Role: RoleViewer},
			{Name: "boss", Token: "admin-token", Role: RoleAdmin},
		},
	})
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/debug/pprof/heap", nil)
	req.Header.Set("Authorization", "Bearer viewer")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer status=%d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/v1/debug/pprof/heap", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin status=%d body=%s", resp.StatusCode, body)
	}
	if len(body) < 32 {
		t.Fatalf("heap profile too small: %d", len(body))
	}
	if cd := resp.Header.Get("Content-Disposition"); cd == "" {
		t.Fatal("missing Content-Disposition")
	}
}
