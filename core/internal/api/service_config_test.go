package api

import (
	"encoding/json"
	"github.com/mengzhihua/monitor/core/internal/sla"
	"net/http"
	"strings"
	"testing"
)

func TestServiceConfigPreconditionsPreviewAndPermissions(t *testing.T) {
	ts, _ := newTestServer(t, Options{OperationsDir: t.TempDir(), Users: []User{{Name: "admin", Token: "a", Role: RoleAdmin}, {Name: "reader", Token: "v", Role: RoleViewer}}})
	var initial sla.ServiceSnapshot
	getJSON(t, ts.URL+"/api/v1/services/config?token=a", &initial)
	request := func(method, path, token, revision string, want int) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"revision": revision, "services": []sla.Service{{Name: "site"}}})
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(string(raw)))
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("%s %s status %d want %d", method, path, res.StatusCode, want)
		}
	}
	request("PUT", "/api/v1/services/config", "v", initial.Revision, 403)
	request("POST", "/api/v1/services/config/preview", "v", initial.Revision, 403)
	request("PUT", "/api/v1/services", "a", "", 428)
	request("POST", "/api/v1/services/config/preview", "a", initial.Revision, 200)
	var after sla.ServiceSnapshot
	getJSON(t, ts.URL+"/api/v1/services/config?token=a", &after)
	if after.Revision != initial.Revision || len(after.Services) != 0 {
		t.Fatal("preview changed catalog")
	}
	request("PUT", "/api/v1/services/config", "a", initial.Revision, 200)
	request("PUT", "/api/v1/services", "a", initial.Revision, 409)
	request("POST", "/api/v1/services/config/preview", "a", initial.Revision, 409)
}
