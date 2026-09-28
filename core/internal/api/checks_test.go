package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/mengzhihua/monitor/core/internal/collect"
)

func TestChecksAPI(t *testing.T) {
	name := "nightly-backup"
	t.Cleanup(func() { collect.Checks().Remove(name) })
	ts, reg := newTestServer(t, Options{
		Users: []User{
			{Name: "Admin", Token: "admin", Role: RoleAdmin},
			{Name: "Reader", Token: "viewer", Role: RoleViewer},
			{Name: "Oncall", Token: "operator", Role: RoleTroubleshooter},
		},
	})
	post := func(token, raw, query string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/checks"+query, strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := post("", `{"name":"nightly-backup","status":"ok"}`, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous status %d", resp.StatusCode)
	}
	resp = post("viewer", `{"name":"nightly-backup","status":"warning"}`, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer post %d", resp.StatusCode)
	}
	resp = post("admin", `{"name":"bad name","status":"ok"}`, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad name %d", resp.StatusCode)
	}
	resp = post("admin", `{"name":"nightly-backup","status":"warning","extra":1}`, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field %d", resp.StatusCode)
	}
	resp = post("admin", `{"name":"nightly-backup","status":"warning","value":26,"ttl":"1m","message":"late"}`, "?node=remote")
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("remote post %d", resp.StatusCode)
	}

	resp = post("admin", `{"name":"nightly-backup","status":"warning","value":26,"ttl":"1m","message":"late"}`, "")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("admin post %d %s", resp.StatusCode, body)
	}
	var row collect.ExternalCheck
	if err := json.NewDecoder(resp.Body).Decode(&row); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	if row.Status != "warning" || row.Chart != "check."+name || row.Value == nil || *row.Value != 26 {
		t.Fatalf("row = %+v", row)
	}
	ch, ok := reg.Chart("check." + name)
	if !ok || ch.Context != "check.status" {
		t.Fatalf("chart = %+v ok=%v", ch, ok)
	}
	if _, last := ch.LastValues(); last["status"] != 1 || last["value"] != 26 {
		t.Fatalf("last = %v", last)
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/checks?node=remote", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer viewer")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("remote get %d", resp.StatusCode)
	}

	req, err = http.NewRequest(http.MethodGet, ts.URL+"/api/v1/checks", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer viewer")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("viewer get %d", resp.StatusCode)
	}
	var listed struct {
		Checks []collect.ExternalCheck `json:"checks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	var found bool
	for _, item := range listed.Checks {
		if item.Name == name && item.Status == "warning" {
			found = true
		}
	}
	if !found {
		t.Fatalf("list = %+v", listed.Checks)
	}

	resp = post("operator", `{"name":"nightly-backup","status":"ok","ttl":"1m"}`, "")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("operator post %d %s", resp.StatusCode, body)
	}
	resp.Body.Close()
	if _, last := ch.LastValues(); last["status"] != 0 {
		t.Fatalf("operator status = %v", last)
	}
}
