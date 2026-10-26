package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mengzhihua/monitor/core/internal/hub"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestHubHostGroupsProxiesAndLeader(t *testing.T) {
	org, err := hub.OpenOrg(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"proxied":true}`)
	}))
	t.Cleanup(upstream.Close)
	ts, _ := newTestServer(t, Options{Org: org, Users: []User{
		{Name: "admin", Token: "admin", Role: RoleAdmin},
		{Name: "viewer", Token: "viewer", Role: RoleViewer},
	}})
	post := func(token, path, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	if resp := post("viewer", "/api/v1/hub/host-groups", `{"name":"linux"}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer create %d", resp.StatusCode)
	}
	resp := post("admin", "/api/v1/hub/host-groups", `{"name":"linux"}`)
	var parent hub.HostGroup
	if err := jsonNew(resp, &parent); err != nil || parent.ID == "" {
		t.Fatalf("create group %v %+v", err, parent)
	}
	childResp := post("admin", "/api/v1/hub/host-groups", `{"name":"web","parent_id":"`+parent.ID+`"}`)
	var child hub.HostGroup
	if err := jsonNew(childResp, &child); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/hub/host-groups?id="+child.ID, strings.NewReader(`{"name":"web","parent_id":"`+parent.ID+`","nodes":["n1"]}`))
	req.Header.Set("Authorization", "Bearer admin")
	putResp, err := http.DefaultClient.Do(req)
	if err != nil || putResp.StatusCode != http.StatusOK {
		t.Fatalf("put group %v %d", err, putResp.StatusCode)
	}
	putResp.Body.Close()
	s := &Server{reg: registry.New(&registry.Host{ID: "hub"}, nil), opt: Options{Org: org}}
	if !s.roomAllowed(User{Rooms: []string{"none"}, HostGroups: []string{parent.ID}}, "n1") {
		t.Fatal("parent host group did not grant the child node")
	}
	if s.roomAllowed(User{Rooms: []string{"none"}, HostGroups: []string{parent.ID}}, "n2") {
		t.Fatal("unrelated node was granted")
	}
	proxyResp := post("admin", "/api/v1/hub/proxies", `{"name":"edge","url":"`+upstream.URL+`","mode":"passive","nodes":["n1"]}`)
	var proxy hub.Proxy
	if err := jsonNew(proxyResp, &proxy); err != nil || proxy.Token == "" {
		t.Fatalf("create proxy %v %+v", err, proxy)
	}
	var listed struct {
		Proxies []hub.Proxy `json:"proxies"`
	}
	if resp := getJSON(t, ts.URL+"/api/v1/hub/proxies?token=admin", &listed); resp.StatusCode != 200 || len(listed.Proxies) != 1 || listed.Proxies[0].Token != "" {
		t.Fatalf("list proxies %+v %d", listed, resp.StatusCode)
	}
	dataReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/data?node=n1&chart=system.ram", nil)
	dataReq.Header.Set("Authorization", "Bearer admin")
	dataResp, err := http.DefaultClient.Do(dataReq)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(dataResp.Body)
	dataResp.Body.Close()
	if dataResp.StatusCode != 200 || !strings.Contains(string(body), `"proxied":true`) {
		t.Fatalf("proxy data %d %s", dataResp.StatusCode, body)
	}
	if resp := getJSON(t, ts.URL+"/api/v1/hub/leader?token=viewer", nil); resp.StatusCode != 200 {
		t.Fatalf("leader %d", resp.StatusCode)
	}
	sp, err := org.CreateSpace("lab")
	if err != nil {
		t.Fatal(err)
	}
	renamed, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/hub/spaces?id="+sp.ID, strings.NewReader(`{"name":"laboratory"}`))
	renamed.Header.Set("Authorization", "Bearer admin")
	renamedResp, err := http.DefaultClient.Do(renamed)
	if err != nil || renamedResp.StatusCode != 200 {
		t.Fatalf("rename space %v %d", err, renamedResp.StatusCode)
	}
	renamedResp.Body.Close()
}

func jsonNew(resp *http.Response, out any) error {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return &statusError{resp.StatusCode, string(body)}
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string { return http.StatusText(e.code) + ": " + e.body }
