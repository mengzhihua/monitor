package api

import (
	"net/http"
	"strings"
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

func TestNotificationTestAPIRequiresAdminAndConfiguredChannel(t *testing.T) {
	e, err := health.New(nil, nil, health.Options{Notifiers: []health.Notifier{&health.EmailNotifier{Server: "unused:25"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ts, _ := newTestServer(t, Options{Health: e, Users: []User{
		{Name: "Admin", Token: "admin", Role: RoleAdmin}, {Name: "Reader", Token: "viewer", Role: RoleViewer}, {Name: "Oncall", Token: "operator", Role: RoleTroubleshooter},
	}})
	for _, tc := range []struct {
		token, query, body string
		status             int
	}{
		{"", "", `{"channel":"email"}`, 401}, {"viewer", "", `{"channel":"email"}`, 403}, {"operator", "", `{"channel":"email"}`, 403},
		{"admin", "?node=remote", `{"channel":"email"}`, 400}, {"admin", "", `{"channel":"unknown"}`, 400},
		{"admin", "", `{"channel":"email","to":"attacker@example.com"}`, 400}, {"admin", "", `{"channel":"email"} {}`, 400},
		{"admin", "", `{}`, 400}, {"admin", "", `{"channel":"email"}`, 202}, {"admin", "", `{"channel":"email"}`, 429},
	} {
		req, _ := http.NewRequest("POST", ts.URL+"/api/v1/operations/notifications/test"+tc.query, strings.NewReader(tc.body))
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != tc.status {
			t.Fatalf("%s %s: got %d want %d", tc.token, tc.query, r.StatusCode, tc.status)
		}
		if tc.status == 429 && r.Header.Get("Retry-After") != "30" {
			t.Fatal("missing retry hint")
		}
	}
	for _, tc := range []struct {
		token   string
		allowed bool
	}{{"admin", true}, {"viewer", false}, {"operator", false}} {
		var snap struct {
			CanTest bool `json:"can_test"`
		}
		getJSON(t, ts.URL+"/api/v1/operations/notifications?token="+tc.token, &snap)
		if snap.CanTest != tc.allowed {
			t.Fatal("wrong test capability", tc.token)
		}
	}
}
