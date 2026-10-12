package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/hub"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/stream"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

type roomFixture struct {
	s                  *Server
	org                *hub.Org
	allowed, forbidden string
	at                 int64
}

func newRoomFixture(t *testing.T) roomFixture {
	t.Helper()
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	org, err := hub.OpenOrg(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sp, err := org.CreateSpace("scope-space")
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := org.CreateRoom(sp.ID, "allowed-room")
	if err != nil {
		t.Fatal(err)
	}
	forbidden, err := org.CreateRoom(sp.ID, "forbidden-room")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = org.SetRoomNodes(allowed.ID, []string{"allowed-node"}); err != nil {
		t.Fatal(err)
	}
	if _, err = org.SetRoomNodes(forbidden.ID, []string{"forbidden-node", "same-host"}); err != nil {
		t.Fatal(err)
	}
	nodes, err := hub.Open(db, t.TempDir(), hub.Options{Keys: []string{"room-fixture-stream"}})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Unix() - 2
	chart := func() *registry.Chart {
		return &registry.Chart{ID: "scope.metric", Context: "scope.metric", Dimensions: []*registry.Dimension{{ID: "value"}}}
	}
	ingest := httptest.NewServer(http.HandlerFunc(nodes.HandleStream))
	defer ingest.Close()
	for i, id := range []string{"allowed-node", "forbidden-node", "unassigned-node", "same-host"} {
		ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ingest.URL, "http"), http.Header{"Authorization": []string{"Bearer room-fixture-stream"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := ws.WriteJSON(stream.Frame{Type: stream.TypeHello, Host: &registry.Host{ID: id, Hostname: id, UpdateEvery: 1}}); err != nil {
			t.Fatal(err)
		}
		_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		var welcome stream.Frame
		if err := ws.ReadJSON(&welcome); err != nil || welcome.Type != stream.TypeWelcome {
			t.Fatalf("stream attach: %+v %v", welcome, err)
		}
		node, ok := nodes.Get(id)
		if !ok {
			t.Fatal("stream node missing")
		}
		node.Registry().AddChart(chart())
		if err = node.Registry().Collect("scope.metric", time.Unix(at, 0), map[string]float64{"value": float64(i + 10)}); err != nil {
			t.Fatal(err)
		}
		_ = ws.Close()
	}
	reg := registry.New(&registry.Host{ID: "same-host", Hostname: "local-host", UpdateEvery: 1}, db)
	reg.AddChart(chart())
	if err = reg.Collect("scope.metric", time.Unix(at, 0), map[string]float64{"value": 5}); err != nil {
		t.Fatal(err)
	}
	templates, err := hub.OpenTemplates(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(reg, db, collect.NewScheduler(reg, nil, collect.Options{Names: []string{"none"}}), Options{Org: org, Nodes: nodes, Templates: templates,
		Users: []User{{Name: "scoped", Token: "scoped-token", Role: RoleViewer, Rooms: []string{allowed.ID}},
			{Name: "scoped-admin", Token: "scoped-admin-token", Role: RoleAdmin, Rooms: []string{allowed.ID}},
			{Name: "admin", Token: "admin-token", Role: RoleAdmin}, {Name: "wildcard", Token: "wildcard-token", Role: RoleViewer, Rooms: []string{"*"}}}})
	if err != nil {
		t.Fatal(err)
	}
	return roomFixture{s: s, org: org, allowed: allowed.ID, forbidden: forbidden.ID, at: at}
}

func TestRoomScopeClaimCredentialsRequireAdminAndActualRoom(t *testing.T) {
	f := newRoomFixture(t)
	space, _ := f.org.Membership("allowed-node")
	allowed, err := f.org.IssueClaim(space, f.allowed, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	forbidden, err := f.org.IssueClaim(space, f.forbidden, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	const path = "/api/v1/hub/claim-tokens"
	for _, token := range []string{"scoped-token", "wildcard-token"} {
		viewRequest(t, f.s, "GET", path, token, "", 403)
	}
	w := viewRequest(t, f.s, "GET", path+"?node=local", "scoped-admin-token", "", 200)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("claim credentials may be cached")
	}
	if !strings.Contains(w.Body.String(), allowed.Token) || strings.Contains(w.Body.String(), forbidden.Token) {
		t.Fatal("claim list ignored administrator room scope")
	}
	w = viewRequest(t, f.s, "GET", path, "admin-token", "", 200)
	if !strings.Contains(w.Body.String(), forbidden.Token) {
		t.Fatal("global administrator lost claim access")
	}
	body := fmt.Sprintf(`{"space_id":%q,"room_id":%q}`, space, f.forbidden)
	viewRequest(t, f.s, "POST", path+"?node=local", "scoped-admin-token", body, 403)
	body = fmt.Sprintf(`{"space_id":%q,"room_id":%q}`, space, f.allowed)
	viewRequest(t, f.s, "POST", path, "scoped-admin-token", body, 200)
	// Enrollment remains public, with the explicit one-time token as its
	// credential. Listing restrictions must not prevent legitimate redemption.
	body = fmt.Sprintf(`{"token":%q,"node_id":"new-enrolled-node"}`, allowed.Token)
	w = viewRequest(t, f.s, "POST", "/api/v1/claim", "", body, 200)
	if !strings.Contains(w.Body.String(), `"api_key"`) {
		t.Fatal("authorized public enrollment failed")
	}
	viewRequest(t, f.s, "POST", "/api/v1/claim", "", body, 400)
}

func TestRoomScopeFiltersAggregatesAndCatalogs(t *testing.T) {
	f := newRoomFixture(t)
	paths := []string{"/api/v1/nodes", "/api/v2/nodes?contexts=true", "/api/v3/nodes?contexts=true", "/api/v1/operations", "/api/v1/hub/console", "/api/v1/hub/inventory", "/metrics"}
	for _, version := range []int{1, 2, 3} {
		for _, group := range []string{"node", "node,dimension"} {
			paths = append(paths, fmt.Sprintf("/api/v%d/data?context=scope.metric&group_by=%s&after=%d&before=%d", version, group, f.at-1, f.at))
		}
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			for _, token := range []string{"scoped-token", "admin-token", "wildcard-token"} {
				w := viewRequest(t, f.s, "GET", path, token, "", http.StatusOK)
				body := w.Body.String()
				if !strings.Contains(body, "allowed-node") || !strings.Contains(body, "unassigned-node") {
					t.Fatalf("missing visible node: %s", body)
				}
				for _, hidden := range []string{"forbidden-node", "same-host"} {
					if got := strings.Contains(body, hidden); got != (token != "scoped-token") {
						t.Fatalf("%s exposed=%t for %s: %s", hidden, got, token, body)
					}
				}
			}
		})
	}
	for _, path := range []string{"/api/v1/hub/rooms", "/api/v1/hub/console"} {
		w := viewRequest(t, f.s, "GET", path, "scoped-token", "", 200)
		if strings.Contains(w.Body.String(), f.forbidden) {
			t.Fatalf("forbidden room exposed by %s", path)
		}
	}
	for _, node := range []string{"forbidden-node", "same-host", "unknown-node"} {
		viewRequest(t, f.s, "GET", "/api/v1/charts?node="+node, "scoped-token", "", 403)
	}
	for _, node := range []string{"local", "allowed-node", "unassigned-node"} {
		viewRequest(t, f.s, "GET", "/api/v1/charts?node="+node, "scoped-token", "", 200)
	}
	viewRequest(t, f.s, "GET", "/api/v1/charts?node=unknown-node", "admin-token", "", 404)
}

func TestRoomScopeLiveSwitchAndMembershipRevocation(t *testing.T) {
	f := newRoomFixture(t)
	ts := httptest.NewServer(f.s.Handler())
	defer ts.Close()
	headers := http.Header{"Authorization": []string{"Bearer scoped-token"}}
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/api/v1/live", headers)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	read := func() map[string]any {
		t.Helper()
		_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		var message map[string]any
		if err := ws.ReadJSON(&message); err != nil {
			t.Fatal(err)
		}
		return message
	}
	switchTo := func(node string) {
		t.Helper()
		if err := ws.WriteJSON(map[string]any{"node": node}); err != nil {
			t.Fatal(err)
		}
	}
	switchTo("forbidden-node")
	if read()["error"] == nil {
		t.Fatal("forbidden in-band switch accepted")
	}
	f.s.PublishNodeSample("forbidden-node", "scope.metric", f.at, map[string]float64{"value": 999})
	f.s.PublishNodeSample("", "scope.metric", f.at, map[string]float64{"value": 5})
	if got := read(); got["node"] != nil || got["chart"] != "scope.metric" {
		t.Fatalf("forbidden sample leaked: %+v", got)
	}
	switchTo("allowed-node")
	switchTo("unknown-barrier")
	if read()["error"] == nil {
		t.Fatal("missing subscription barrier")
	}
	f.s.PublishNodeSample("allowed-node", "scope.metric", f.at, map[string]float64{"value": 10})
	if got := read(); got["node"] != "allowed-node" {
		t.Fatalf("allowed sample missing: %+v", got)
	}
	if _, err := f.org.SetRoomNodes(f.allowed, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.org.SetRoomNodes(f.forbidden, []string{"forbidden-node", "same-host", "allowed-node"}); err != nil {
		t.Fatal(err)
	}
	f.s.PublishNodeSample("allowed-node", "scope.metric", f.at+1, map[string]float64{"value": 999})
	f.s.PublishNodeAlarm("allowed-node", health.LogEntry{Name: "private-alarm"})
	switchTo("unknown-barrier")
	if got := read(); got["error"] == nil {
		t.Fatalf("revoked live data leaked: %+v", got)
	}
}

func TestRoomScopeLiveWriterDropsQueuedRevokedData(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		c := &liveConn{ws: ws, send: make(chan liveFrame, 2), allowed: func(node string) bool { return node == "" }}
		// The first frame was queued while the room was accessible; by the
		// time the writer runs, only local data is still authorized.
		c.send <- liveFrame{node: "revoked-node", payload: []byte(`{"node":"revoked-node","chart":"private"}`)}
		c.send <- liveFrame{payload: []byte(`{"chart":"local"}`)}
		close(c.send)
		(&liveHub{}).writer(c)
	}))
	defer ts.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	var got map[string]string
	if err := ws.ReadJSON(&got); err != nil || got["chart"] != "local" {
		t.Fatalf("queued revoked frame escaped: %+v err=%v", got, err)
	}
}

func TestRoomScopeClusterUsesPeerMembership(t *testing.T) {
	f := newRoomFixture(t)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/nodes" {
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []hub.Info{{ID: "peer-allowed", Hostname: "peer-allowed", RoomID: f.allowed}, {ID: "peer-forbidden", Hostname: "peer-forbidden", RoomID: f.forbidden}, {ID: "peer-unassigned", Hostname: "peer-unassigned"}, {ID: f.s.reg.Host.ID, Hostname: "peer-host-collision", RoomID: f.forbidden}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"node": r.URL.Query().Get("node")})
	}))
	defer peer.Close()
	cluster := hub.NewCluster([]string{peer.URL}, "", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cluster.Run(ctx)
	waitFor(t, "peer catalog", func() bool { return cluster.Has("peer-forbidden") })
	f.s.opt.Cluster = cluster
	w := viewRequest(t, f.s, "GET", "/api/v1/nodes", "scoped-token", "", 200)
	if !strings.Contains(w.Body.String(), "peer-allowed") || strings.Contains(w.Body.String(), "peer-forbidden") {
		t.Fatalf("peer room erased or ignored: %s", w.Body.String())
	}
	viewRequest(t, f.s, "GET", "/api/v1/charts?node=peer-forbidden", "scoped-token", "", 403)
	viewRequest(t, f.s, "GET", "/api/v1/charts?node=peer-allowed", "scoped-token", "", 200)
	// Exercise the real authenticated ring receiver. Once replicas exist the
	// local Nodes entry wins over PeerInfos, but its missing room must not grant
	// access to a peer's restricted node or to an unknown orphaned replica.
	f.s.opt.PeerToken = "room-ring-token"
	for _, id := range []string{"peer-allowed", "peer-forbidden", "peer-unassigned", "peer-unknown"} {
		payload := hub.RingPayload{Host: registry.Host{ID: id, Hostname: id, UpdateEvery: 1}, Charts: []*stream.ChartDef{{ID: "scope.metric", Context: "scope.metric", Dimensions: []stream.DimDef{{ID: "value"}}}}}
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		viewRequest(t, f.s, "POST", "/api/v1/hub/ring", "room-ring-token", string(encoded), 200)
		node, _ := f.s.opt.Nodes.Get(id)
		if err := node.Registry().Collect("scope.metric", time.Unix(f.at, 0), map[string]float64{"value": 21}); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/api/v1/nodes", "/api/v1/hub/inventory", "/api/v1/operations", "/metrics", fmt.Sprintf("/api/v3/data?context=scope.metric&group_by=node&after=%d&before=%d", f.at-1, f.at)} {
		w := viewRequest(t, f.s, "GET", path, "scoped-token", "", 200)
		for _, visible := range []string{"peer-allowed", "peer-unassigned"} {
			if !strings.Contains(w.Body.String(), visible) {
				t.Fatalf("%s missing %s: %s", path, visible, w.Body.String())
			}
		}
		for _, hidden := range []string{"peer-forbidden", "peer-unknown"} {
			if strings.Contains(w.Body.String(), hidden) {
				t.Fatalf("%s leaked replica %s", path, hidden)
			}
		}
	}
	viewRequest(t, f.s, "GET", "/api/v1/charts?node=peer-forbidden", "scoped-token", "", 403)
	viewRequest(t, f.s, "GET", "/api/v1/charts?node=peer-unknown", "scoped-token", "", 403)
	viewRequest(t, f.s, "GET", "/api/v1/charts?node=peer-unknown", "admin-token", "", 200)
	waitFor(t, "same-host stream detached", func() bool {
		n, _ := f.s.opt.Nodes.Get(f.s.reg.Host.ID)
		return n.Status(time.Now()) == hub.StatusOffline
	})
	if err := f.s.opt.Nodes.Forget(f.s.reg.Host.ID); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"scoped-token", "admin-token"} {
		w := viewRequest(t, f.s, "GET", "/api/v1/nodes?contexts=true", token, "", 200)
		if strings.Contains(w.Body.String(), "peer-host-collision") {
			t.Fatal("unaddressable peer inherited local host alias permissions")
		}
	}
	f.s.opt.Cluster = nil
	viewRequest(t, f.s, "GET", "/api/v1/charts?node=peer-allowed", "scoped-token", "", 403)
}

func TestRoomScopeLegacyConflictHiddenUntilExplicitRepair(t *testing.T) {
	f := newRoomFixture(t)
	dir := t.TempDir()
	legacy, err := json.Marshal(map[string]any{"spaces": []hub.Space{{ID: "s", Name: "legacy"}}, "rooms": []hub.Room{{ID: f.allowed, SpaceID: "s", Name: "allowed", Nodes: []string{"allowed-node", "forbidden-node"}}, {ID: f.forbidden, SpaceID: "s", Name: "forbidden", Nodes: []string{"forbidden-node"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "org.json"), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	org, err := hub.OpenOrg(dir)
	if err != nil {
		t.Fatal(err)
	}
	f.s.opt.Org = org
	for _, path := range []string{"/api/v1/nodes", "/api/v1/hub/rooms", "/api/v1/hub/console", "/api/v1/hub/inventory", "/metrics"} {
		w := viewRequest(t, f.s, "GET", path, "scoped-token", "", 200)
		if strings.Contains(w.Body.String(), "forbidden-node") {
			t.Fatalf("%s exposed conflicting membership", path)
		}
	}
	w := viewRequest(t, f.s, "GET", "/api/v1/nodes", "admin-token", "", 200)
	if !strings.Contains(w.Body.String(), `"room_conflict":true`) {
		t.Fatal("administrator cannot diagnose legacy conflict")
	}
	viewRequest(t, f.s, "GET", "/api/v1/charts?node=forbidden-node", "scoped-token", "", 403)
	if _, err := org.SetRoomNodes(f.allowed, []string{"allowed-node", "forbidden-node"}); err != nil {
		t.Fatal(err)
	}
	viewRequest(t, f.s, "GET", "/api/v1/charts?node=forbidden-node", "scoped-token", "", 200)
}
