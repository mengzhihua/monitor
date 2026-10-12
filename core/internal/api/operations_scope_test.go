package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/hub"
	"github.com/mengzhihua/monitor/core/internal/operations"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/stream"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func TestOperationsRoomScopeCoversActivityHistoryAndExports(t *testing.T) {
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	nodes, err := hub.Open(db, t.TempDir(), hub.Options{Keys: []string{"operations-scope-stream"}})
	if err != nil {
		t.Fatal(err)
	}
	ingest := httptest.NewServer(http.HandlerFunc(nodes.HandleStream))
	defer ingest.Close()
	for _, id := range []string{"allowed-node", "denied-node", "hub-id", "unassigned-node"} {
		ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ingest.URL, "http"), http.Header{"Authorization": []string{"Bearer operations-scope-stream"}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ws.Close() })
		if err := ws.WriteJSON(stream.Frame{Type: stream.TypeHello, Host: &registry.Host{ID: id, Hostname: id, UpdateEvery: 1}}); err != nil {
			t.Fatal(err)
		}
		_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		var welcome stream.Frame
		if err := ws.ReadJSON(&welcome); err != nil || welcome.Type != stream.TypeWelcome {
			t.Fatalf("register direct agent: %+v %v", welcome, err)
		}
		if err := ws.Close(); err != nil {
			t.Fatal(err)
		}
	}
	org, err := hub.OpenOrg(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	space, err := org.CreateSpace("scope-test")
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := org.CreateRoom(space.ID, "allowed")
	if err != nil {
		t.Fatal(err)
	}
	denied, err := org.CreateRoom(space.ID, "denied")
	if err != nil {
		t.Fatal(err)
	}
	setMembership := func(moved bool) {
		t.Helper()
		a, b := []string{"allowed-node"}, []string{"denied-node", "hub-id"}
		if moved {
			a, b = nil, append(b, "allowed-node")
		}
		if _, err := org.SetRoomNodes(allowed.ID, a); err != nil {
			t.Fatal(err)
		}
		if _, err := org.SetRoomNodes(denied.ID, b); err != nil {
			t.Fatal(err)
		}
	}
	setMembership(false)
	reg := registry.New(&registry.Host{ID: "hub-id", Hostname: "hub"}, db)
	sched := collect.NewScheduler(reg, nil, collect.Options{Names: []string{"none"}})
	s, err := New(reg, db, sched, Options{Nodes: nodes, Org: org, Token: "unrestricted", Users: []User{
		{Name: "room-reader", Token: "scoped", Role: RoleViewer, Rooms: []string{allowed.ID}},
		{Name: "other-reader", Token: "other", Role: RoleViewer, Rooms: []string{denied.ID}},
		{Name: "global-reader", Token: "wildcard", Role: RoleViewer, Rooms: []string{"*"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for i, node := range []string{"", "allowed-node", "allowed-node", "denied-node", "retired-node", "hub-id", "unassigned-node"} {
		if _, err := s.operations.Update(fmt.Sprintf("episode-%d", i), "operator", "comment", node+"_private_note", 0, operations.Target{Node: node, Hostname: node, Name: "ram", Severity: "WARNING"}); err != nil {
			t.Fatal(err)
		}
	}
	get := func(path, token string, want int) []byte {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s status=%d want=%d: %s", path, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	assertPrivate := func(body []byte) {
		t.Helper()
		for _, hidden := range []string{"denied-node_private_note", "retired-node_private_note", "hub-id_private_note"} {
			if strings.Contains(string(body), hidden) {
				t.Fatal("hidden history exposed", hidden)
			}
		}
	}
	var overview operationsSnapshot
	body := get("/api/v1/operations", "scoped", 200)
	assertPrivate(body)
	if err := json.Unmarshal(body, &overview); err != nil || len(overview.Activity) != 4 {
		t.Fatal("incorrect visible activity", overview.Activity, err)
	}
	const base = "/api/v1/operations/history"
	var first operations.HistoryPage
	if err := json.Unmarshal(get(base+"?limit=1", "scoped", 200), &first); err != nil || first.Total != 4 || first.Stored != 4 || first.NextCursor == "" {
		t.Fatal("incorrect scoped history", first, err)
	}
	assertPrivate(get(base+"?limit=1&cursor="+url.QueryEscape(first.NextCursor), "scoped", 200))
	for _, format := range []string{"json", "csv"} {
		body := get(base+"/export?format="+format+"&snapshot="+first.Snapshot, "scoped", 200)
		assertPrivate(body)
		if format == "json" {
			var exported operations.HistoryPage
			if err := json.Unmarshal(body, &exported); err != nil || len(exported.Records) != 4 {
				t.Fatal("incomplete scoped export", exported, err)
			}
		}
	}
	get(base+"?cursor="+url.QueryEscape(first.NextCursor), "other", 409)
	get(base+"/export?format=json&snapshot="+first.Snapshot, "unrestricted", 409)
	for _, token := range []string{"unrestricted", "wildcard"} {
		var page operations.HistoryPage
		if err := json.Unmarshal(get(base, token, 200), &page); err != nil || page.Total != 7 || page.Stored != 7 {
			t.Fatal("unrestricted history lost", page, err)
		}
	}
	for _, node := range []string{"denied-node", "retired-node", "hub-id"} {
		get(base+"?node="+node, "scoped", 403)
	}
	setMembership(true)
	get(base+"?cursor="+url.QueryEscape(first.NextCursor), "scoped", 409)
	get(base+"/export?format=json&snapshot="+first.Snapshot, "scoped", 409)
	var fresh operations.HistoryPage
	if err := json.Unmarshal(get(base, "scoped", 200), &fresh); err != nil || fresh.Total != 2 || fresh.Stored != 2 {
		t.Fatal("membership change retained hidden history", fresh, err)
	}
}
