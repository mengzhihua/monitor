package api

import (
	"net/http"
	"testing"

	"github.com/mengzhihua/monitor/core/internal/health"
)

func TestNotificationDiagnosticsAccessAndLocalScope(t *testing.T) {
	e, err := health.New(nil, nil, health.Options{Hostname: "local-engine"})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ts, _ := newTestServer(t, Options{Health: e, Users: []User{{Name: "reader", Token: "viewer", Role: RoleViewer}}})
	path := ts.URL + "/api/v1/operations/notifications"
	if r := getJSON(t, path, nil); r.StatusCode != http.StatusUnauthorized {
		t.Fatal(r.StatusCode)
	}
	var snap health.NotificationSnapshot
	r := getJSON(t, path+"?token=viewer&node=unknown", &snap)
	if r.StatusCode != 200 || r.Header.Get("Cache-Control") != "no-store" || !snap.Available || snap.Scope != "local" || snap.Hostname != "local-engine" || snap.Recent == nil || snap.Channels == nil {
		t.Fatal(r.StatusCode, snap)
	}
	r, err = http.Post(path+"?token=viewer", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode < 400 {
		t.Fatal("read-only endpoint accepted mutation")
	}
	ts2, _ := newTestServer(t, Options{Token: "admin"})
	getJSON(t, ts2.URL+"/api/v1/operations/notifications?token=admin", &snap)
	if snap.Available || snap.Recent == nil || snap.Scope != "local" {
		t.Fatal(snap)
	}
}
