package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

const testKey = "test-stream-key"

// newHub starts a hub API server whose stream ingestion accepts testKey.
func newHub(t *testing.T, extra Options) (*httptest.Server, *hub.Nodes) {
	t.Helper()
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var srv *Server
	nodes, err := hub.Open(db, t.TempDir(), hub.Options{Keys: []string{testKey},
		OnSample: func(n, c string, ts int64, v map[string]float64) { srv.PublishNodeSample(n, c, ts, v) },
		OnAlarm:  func(n string, e health.LogEntry) { srv.PublishNodeAlarm(n, e) }})
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New(&registry.Host{ID: "hub-id", Hostname: "hub", OS: "linux", UpdateEvery: 1}, db)
	sched := collect.NewScheduler(reg, nil, collect.Options{Names: []string{"none"}})
	extra.Mode, extra.Nodes, extra.StartedAt = "hub", nodes, time.Now()
	srv, err = New(reg, db, sched, extra)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, nodes
}

// newAgent builds a registry+tsdb with one chart and a stream client to hubURL.
func newAgent(t *testing.T, hubURL string, fns []collect.Function) (*registry.Registry, *tsdb.Store, *stream.Client) {
	t.Helper()
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{ID: "agent-1", Hostname: "agent", OS: "linux", UpdateEvery: 1}, db)
	reg.AddChart(&registry.Chart{ID: "system.ram", Context: "system.ram", Family: "ram", Title: "RAM", Units: "MiB",
		Type: registry.Stacked, Priority: 200, Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "free"}}})
	c := stream.NewClient(reg, db, stream.ClientOptions{Destinations: []string{hubURL}, APIKey: testKey, Version: "t",
		Timeout: 5 * time.Second, Functions: func() []collect.Function { return fns }})
	return reg, db, c
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestHubStreamEndToEnd(t *testing.T) {
	hubTS, nodes := newHub(t, Options{})
	fns := []collect.Function{{Name: "echo", Help: "echo args", Timeout: 5,
		Run: func(_ context.Context, args map[string]string) (any, error) { return map[string]any{"args": args}, nil }}}
	reg, db, client := newAgent(t, hubTS.URL, fns)

	// history collected before the hub was reachable → replicated on connect
	now := time.Now().Truncate(time.Second)
	for i := 10; i >= 1; i-- {
		_ = reg.Collect("system.ram", now.Add(-time.Duration(i)*time.Second), map[string]float64{"used": float64(100 - i), "free": 50})
	}
	_ = db

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go client.Run(ctx)

	waitFor(t, "node online", func() bool {
		n, ok := nodes.Get("agent-1")
		return ok && n.Status(time.Now()) != hub.StatusOffline
	})
	node, _ := nodes.Get("agent-1")
	waitFor(t, "replication", func() bool {
		_, last, ok := node.DB().Bounds(registry.SeriesID("system.ram", "used"))
		return ok && last == now.Add(-time.Second).Unix()
	})

	// live sample after connect
	_ = reg.Collect("system.ram", now.Add(time.Second), map[string]float64{"used": 200, "free": 50})
	waitFor(t, "live sample", func() bool {
		_, last, ok := node.DB().Bounds(registry.SeriesID("system.ram", "used"))
		return ok && last == now.Add(time.Second).Unix()
	})
	if node.Status(time.Now()) != hub.StatusLive {
		t.Fatalf("status = %s", node.Status(time.Now()))
	}

	// /api/v1/nodes lists local + agent
	var nl struct {
		Nodes []struct {
			ID, Hostname, Status string
			Local                bool
			ChartsCount          int `json:"charts_count"`
		}
	}
	getJSON(t, hubTS.URL+"/api/v1/nodes", &nl)
	if len(nl.Nodes) != 2 || !nl.Nodes[0].Local || nl.Nodes[1].ID != "agent-1" || nl.Nodes[1].Status != "live" || nl.Nodes[1].ChartsCount != 1 {
		t.Fatalf("nodes = %+v", nl.Nodes)
	}

	// node-scoped data query: 11 samples, values 90..99 then 200
	var data struct {
		Node   string
		Result struct{ Data [][]*float64 }
	}
	getJSON(t, hubTS.URL+fmt.Sprintf("/api/v1/data?node=agent-1&chart=system.ram&after=%d&before=%d", now.Unix()-10, now.Unix()+2), &data)
	if data.Node != "agent-1" || len(data.Result.Data) != 11 {
		t.Fatalf("data rows = %d (%+v)", len(data.Result.Data), data)
	}
	last := data.Result.Data[len(data.Result.Data)-1]
	if last[1] == nil || *last[1] != 200 {
		t.Fatalf("last row = %v", last)
	}
	// local host has no such chart; isolation
	if resp := getJSON(t, hubTS.URL+"/api/v1/data?chart=system.ram", nil); resp.StatusCode != 404 {
		t.Fatalf("local data status = %d", resp.StatusCode)
	}
	if resp := getJSON(t, hubTS.URL+"/api/v1/charts?node=nope", nil); resp.StatusCode != 404 {
		t.Fatalf("unknown node status = %d", resp.StatusCode)
	}

	// prometheus export carries node label
	resp, _ := http.Get(hubTS.URL + "/metrics")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `node="agent-1"`) {
		t.Fatalf("metrics missing node label:\n%s", body)
	}

	// function proxy
	var fr struct {
		Node   string
		Result struct{ Args map[string]string }
	}
	if resp := getJSON(t, hubTS.URL+"/api/v1/function?node=agent-1&function=echo&x=1", &fr); resp.StatusCode != 200 {
		t.Fatalf("function status = %d", resp.StatusCode)
	}
	if fr.Node != "agent-1" || fr.Result.Args["x"] != "1" {
		t.Fatalf("function result = %+v", fr)
	}
	if resp := getJSON(t, hubTS.URL+"/api/v1/function?node=agent-1&function=nope", nil); resp.StatusCode != 404 {
		t.Fatalf("unknown function status = %d", resp.StatusCode)
	}

	// alarm forwarding
	client.PublishAlarm(health.LogEntry{UniqueID: 1, AlarmID: 7, When: now.Unix(), Name: "ram_in_use", Chart: "system.ram", Status: health.StatusWarning, Value: 91})
	waitFor(t, "alarm", func() bool { return len(node.Alarms()) == 1 })
	var al struct {
		Alarms  map[string]struct{ Status string }
		Summary struct{ Warning int }
	}
	getJSON(t, hubTS.URL+"/api/v1/alarms?node=agent-1", &al)
	if al.Summary.Warning != 1 || al.Alarms["system.ram.ram_in_use"].Status != "WARNING" {
		t.Fatalf("alarms = %+v", al)
	}

	// live WS scoped to the node
	wsURL := "ws" + strings.TrimPrefix(hubTS.URL, "http") + "/api/v1/live?node=agent-1"
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	_ = reg.Collect("system.ram", now.Add(2*time.Second), map[string]float64{"used": 201, "free": 50})
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	var msg struct {
		Node  string
		Chart string
		V     map[string]float64
	}
	if err := ws.ReadJSON(&msg); err != nil {
		t.Fatal(err)
	}
	if msg.Node != "agent-1" || msg.Chart != "system.ram" || msg.V["used"] != 201 {
		t.Fatalf("live msg = %+v", msg)
	}

	// forgetting a connected node is refused; after disconnect it works
	req, _ := http.NewRequest(http.MethodDelete, hubTS.URL+"/api/v1/nodes?node=agent-1", nil)
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != http.StatusConflict {
		t.Fatalf("forget connected = %d", resp.StatusCode)
	}
	cancel()
	waitFor(t, "offline", func() bool { return node.Status(time.Now()) == hub.StatusOffline })
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("forget offline = %d", resp.StatusCode)
	}
	getJSON(t, hubTS.URL+"/api/v1/nodes", &nl)
	if len(nl.Nodes) != 1 {
		t.Fatalf("nodes after forget = %+v", nl.Nodes)
	}
}

func TestHubReconnectReplicatesGap(t *testing.T) {
	hubTS, nodes := newHub(t, Options{})
	reg, _, client := newAgent(t, hubTS.URL, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go client.Run(ctx)
	waitFor(t, "connect", func() bool { return client.Status().Connected })
	now := time.Now().Truncate(time.Second)
	_ = reg.Collect("system.ram", now, map[string]float64{"used": 1, "free": 1})
	node, _ := nodes.Get("agent-1")
	waitFor(t, "first sample", func() bool {
		_, last, ok := node.DB().Bounds(registry.SeriesID("system.ram", "used"))
		return ok && last == now.Unix()
	})
	cancel()
	waitFor(t, "disconnect", func() bool { return !client.Status().Connected })

	// samples while disconnected go only to the local tsdb; replication
	// covers everything older than the current second, so let the clock pass them
	for i := 1; i <= 2; i++ {
		_ = reg.Collect("system.ram", now.Add(time.Duration(i)*time.Second), map[string]float64{"used": float64(1 + i), "free": 1})
	}
	time.Sleep(time.Until(now.Add(3500 * time.Millisecond)))
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go client.Run(ctx2)
	waitFor(t, "gap replicated", func() bool {
		_, last, ok := node.DB().Bounds(registry.SeriesID("system.ram", "used"))
		return ok && last == now.Add(2*time.Second).Unix()
	})
	pts, _ := node.DB().QueryTier(registry.SeriesID("system.ram", "used"), 0, now.Unix(), now.Add(2*time.Second).Unix())
	if len(pts) != 3 {
		t.Fatalf("hub has %d points after reconnect, want 3", len(pts))
	}
	if st := client.Status(); st.Replicated < 2 || !st.Connected {
		t.Fatalf("status = %+v", st)
	}
}

// An agent on the same machine as the hub shares its machine-id: node=<id>
// must still address the streamed node, not the hub itself.
func TestHubNodeSharesLocalHostID(t *testing.T) {
	hubTS, nodes := newHub(t, Options{})
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{ID: "hub-id", Hostname: "agent-same-box", OS: "linux", UpdateEvery: 1}, db)
	reg.AddChart(&registry.Chart{ID: "app.x", Context: "app.x", Family: "x", Title: "X", Units: "n",
		Dimensions: []*registry.Dimension{{ID: "v"}}})
	client := stream.NewClient(reg, db, stream.ClientOptions{Destinations: []string{hubTS.URL}, APIKey: testKey, Timeout: 5 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go client.Run(ctx)
	waitFor(t, "node online", func() bool {
		n, ok := nodes.Get("hub-id")
		return ok && len(n.Registry().Charts()) == 1
	})

	var cr struct {
		Hostname string
		Charts   map[string]any
	}
	getJSON(t, hubTS.URL+"/api/v1/charts?node=hub-id", &cr)
	if cr.Hostname != "agent-same-box" || len(cr.Charts) != 1 {
		t.Fatalf("node=hub-id resolved to %q with %d charts, want the streamed node", cr.Hostname, len(cr.Charts))
	}
	getJSON(t, hubTS.URL+"/api/v1/charts", &cr)
	if cr.Hostname != "hub" {
		t.Fatalf("local view hostname = %q", cr.Hostname)
	}
}

func TestHubStreamAuth(t *testing.T) {
	hubTS, _ := newHub(t, Options{})
	url := "ws" + strings.TrimPrefix(hubTS.URL, "http") + stream.Path
	if _, resp, err := websocket.DefaultDialer.Dial(url, nil); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no key: err=%v resp=%v", err, resp)
	}
	hdr := http.Header{"Authorization": {"Bearer wrong"}}
	if _, resp, err := websocket.DefaultDialer.Dial(url, hdr); err == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong key: err=%v", err)
	}
	// not a hub → node= rejected
	agentTS, _ := newTestServer(t, Options{})
	if resp := getJSON(t, agentTS.URL+"/api/v1/charts?node=x", nil); resp.StatusCode != 404 {
		t.Fatalf("agent node= status = %d", resp.StatusCode)
	}
	if _, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(agentTS.URL, "http")+stream.Path, nil); err == nil {
		t.Fatal("agent accepted a stream connection")
	}
}

func TestRBAC(t *testing.T) {
	ts, _ := newTestServer(t, Options{Users: []User{
		{Name: "a", Token: "tok-admin", Role: RoleAdmin},
		{Name: "tsh", Token: "tok-tsh", Role: RoleTroubleshooter},
		{Name: "v", Token: "tok-view", Role: RoleViewer},
	}})
	do := func(method, path, tok string) int {
		req, _ := http.NewRequest(method, ts.URL+path, nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	cases := []struct {
		method, path, tok string
		want              int
	}{
		{"GET", "/api/v1/info", "", 401},
		{"GET", "/api/v1/info", "bad", 401},
		{"GET", "/api/v1/info", "tok-view", 200},
		{"GET", "/api/v1/nodes", "tok-view", 200},
		{"GET", "/api/v1/function?function=processes", "tok-view", 403},
		{"GET", "/api/v1/function?function=nope", "tok-tsh", 404},
		{"DELETE", "/api/v1/nodes?node=x", "tok-tsh", 403},
		{"DELETE", "/api/v1/nodes?node=x", "tok-view", 403},
		{"DELETE", "/api/v1/nodes?node=x", "tok-admin", 404},
		{"GET", "/metrics", "tok-view", 200},
		{"GET", "/api/v1/alarms/silence", "tok-view", 404}, // health disabled on this server
		{"POST", "/api/v1/alarms/silence", "tok-view", 403},
		{"POST", "/api/v1/alarms/silence", "tok-tsh", 403},
	}
	for _, c := range cases {
		if got := do(c.method, c.path, c.tok); got != c.want {
			t.Errorf("%s %s as %q = %d, want %d", c.method, c.path, c.tok, got, c.want)
		}
	}
	var info struct{ User struct{ Name, Role string } }
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/info", nil)
	req.Header.Set("Authorization", "Bearer tok-view")
	resp, _ := http.DefaultClient.Do(req)
	_ = json.NewDecoder(resp.Body).Decode(&info)
	if info.User.Name != "v" || info.User.Role != "viewer" {
		t.Fatalf("info.user = %+v", info.User)
	}

	// invalid role rejected at construction
	if _, err := New(registry.New(&registry.Host{}, nil), nil, nil, Options{Users: []User{{Name: "x", Token: "t", Role: "root"}}}); err == nil {
		t.Fatal("expected error for unknown role")
	}
}

// Alarm state is a snapshot on every connect: transitions that happened while
// the agent was offline (or the hub down) converge, and survive hub restart.
func TestHubAlarmSnapshotConverges(t *testing.T) {
	hubTS, nodes := newHub(t, Options{})
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{ID: "agent-1", Hostname: "agent", OS: "linux", UpdateEvery: 1}, db)
	var mu sync.Mutex
	current := []health.Alarm{{ID: 1, Name: "ram_high", Chart: "system.ram", Status: health.StatusWarning, LastStatusChange: 100}}
	client := stream.NewClient(reg, db, stream.ClientOptions{Destinations: []string{hubTS.URL}, APIKey: testKey, Timeout: 5 * time.Second,
		Alarms: func() []health.Alarm { mu.Lock(); defer mu.Unlock(); return append([]health.Alarm(nil), current...) }})
	ctx, cancel := context.WithCancel(context.Background())
	go client.Run(ctx)
	node := waitNode(t, nodes, "agent-1")
	waitFor(t, "snapshot", func() bool {
		a := node.Alarms()
		return len(a) == 1 && a[0].Status == health.StatusWarning && a[0].Hostname == "agent"
	})
	cancel()
	waitFor(t, "disconnect", func() bool { return !client.Status().Connected })

	// offline: ram_high clears, a new alarm raises
	mu.Lock()
	current = []health.Alarm{{ID: 1, Name: "ram_high", Chart: "system.ram", Status: health.StatusClear, LastStatusChange: 200},
		{ID: 2, Name: "cpu_high", Chart: "system.cpu", Status: health.StatusCritical, LastStatusChange: 201}}
	mu.Unlock()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go client.Run(ctx2)
	waitFor(t, "converged", func() bool {
		by := map[string]health.LogEntry{}
		for _, a := range node.Alarms() {
			by[a.Name] = a
		}
		return len(by) == 2 && by["ram_high"].Status == health.StatusClear && by["cpu_high"].Status == health.StatusCritical
	})
	log := node.AlarmLog(0)
	last := log[len(log)-1]
	if got := log[len(log)-2]; got.Name != "ram_high" || got.OldStatus != health.StatusWarning || got.Status != health.StatusClear {
		t.Fatalf("log transition = %+v", got)
	}
	if last.Name != "cpu_high" {
		t.Fatalf("log tail = %+v", last)
	}

	// persisted with the node → visible after a hub restart before the agent reconnects
	if err := nodes.Save(); err != nil {
		t.Fatal(err)
	}
	var lst struct {
		Nodes []struct{ Alarms map[string]int }
	}
	getJSON(t, hubTS.URL+"/api/v1/nodes", &lst)
	if len(lst.Nodes) != 2 || lst.Nodes[1].Alarms["critical"] != 1 {
		t.Fatalf("nodes = %+v", lst.Nodes)
	}
}

func waitNode(t *testing.T, nodes *hub.Nodes, id string) *hub.Node {
	t.Helper()
	var node *hub.Node
	waitFor(t, "node "+id, func() bool {
		n, ok := nodes.Get(id)
		node = n
		return ok && n.Status(time.Now()) != hub.StatusOffline
	})
	return node
}

// A node is bound to the key that registered it: a second agent holding a
// different valid key cannot claim the same id.
func TestHubNodeBoundToKey(t *testing.T) {
	hubTS, nodes := newHubKeys(t, []string{testKey, "other-key"})
	_, _, a := newAgent(t, hubTS.URL, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	waitNode(t, nodes, "agent-1")

	db, _ := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{ID: "agent-1", Hostname: "impostor", OS: "linux", UpdateEvery: 1}, db)
	b := stream.NewClient(reg, db, stream.ClientOptions{Destinations: []string{hubTS.URL}, APIKey: "other-key", Timeout: 5 * time.Second})
	go b.Run(ctx)
	waitFor(t, "impostor rejected", func() bool { return strings.Contains(b.Status().LastError, "different api key") })
	n, _ := nodes.Get("agent-1")
	if n.Host.Hostname != "agent" || !a.Status().Connected {
		t.Fatalf("node taken over: %+v connected=%v", n.Host, a.Status().Connected)
	}
}

func newHubKeys(t *testing.T, keys []string) (*httptest.Server, *hub.Nodes) {
	return newHubOpt(t, hub.Options{Keys: keys})
}

func newHubOpt(t *testing.T, hopt hub.Options) (*httptest.Server, *hub.Nodes) {
	t.Helper()
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	nodes, err := hub.Open(db, t.TempDir(), hopt)
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New(&registry.Host{ID: "hub-id", Hostname: "hub", OS: "linux", UpdateEvery: 1}, db)
	sched := collect.NewScheduler(reg, nil, collect.Options{Names: []string{"none"}})
	srv, err := New(reg, db, sched, Options{Mode: "hub", Nodes: nodes, StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, nodes
}

func TestHubQuotas(t *testing.T) {
	hubTS, nodes := newHubOpt(t, hub.Options{Keys: []string{testKey}, MaxNodes: 1, MaxChartsPerNode: 1, MaxDimsPerChart: 2})
	reg, _, a := newAgent(t, hubTS.URL, nil) // system.ram: 2 dims → fits
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	node := waitNode(t, nodes, "agent-1")
	waitFor(t, "first chart", func() bool { return len(node.Registry().Charts()) == 1 })

	// over the per-node chart limit → ignored; too many dims → ignored
	reg.AddChart(&registry.Chart{ID: "app.two", Context: "app.two", Title: "2", Units: "n", Dimensions: []*registry.Dimension{{ID: "a"}}})
	_ = reg.Collect("app.two", time.Now(), map[string]float64{"a": 1})
	reg.AddChart(&registry.Chart{ID: "app.wide", Context: "app.wide", Title: "w", Units: "n", Dimensions: []*registry.Dimension{{ID: "a"}, {ID: "b"}, {ID: "c"}}})
	_ = reg.Collect("app.wide", time.Now(), map[string]float64{"a": 1, "b": 2, "c": 3})
	time.Sleep(200 * time.Millisecond)
	if got := len(node.Registry().Charts()); got != 1 {
		t.Fatalf("hub accepted %d charts, quota is 1", got)
	}

	// second node over MaxNodes → rejected with an error frame
	db, _ := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	t.Cleanup(func() { _ = db.Close() })
	reg2 := registry.New(&registry.Host{ID: "agent-2", Hostname: "two", OS: "linux", UpdateEvery: 1}, db)
	b := stream.NewClient(reg2, db, stream.ClientOptions{Destinations: []string{hubTS.URL}, APIKey: testKey, Timeout: 5 * time.Second})
	go b.Run(ctx)
	waitFor(t, "node limit", func() bool { return strings.Contains(b.Status().LastError, "node limit") })
	if _, ok := nodes.Get("agent-2"); ok {
		t.Fatal("node registered beyond MaxNodes")
	}
}

// A re-sent definition replaces the hub's copy: renamed/hidden dimensions
// and chart metadata are updated, removed dimensions disappear.
func TestHubChartDefinitionUpdate(t *testing.T) {
	hubTS, nodes := newHub(t, Options{})
	reg, _, a := newAgent(t, hubTS.URL, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	node := waitNode(t, nodes, "agent-1")
	now := time.Now().Truncate(time.Second)
	_ = reg.Collect("system.ram", now, map[string]float64{"used": 1, "free": 2})
	waitFor(t, "chart", func() bool { return len(node.Registry().Charts()) == 1 })

	// agent-side: rename+hide "free", drop nothing yet, retitle
	reg.RemoveChart("system.ram")
	reg.AddChart(&registry.Chart{ID: "system.ram", Context: "system.ram", Family: "ram", Title: "Memory", Units: "MiB", Type: registry.Stacked,
		Priority: 200, Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "free", Name: "available", Hidden: true}}})
	_ = reg.Collect("system.ram", now.Add(time.Second), map[string]float64{"used": 3, "free": 4})
	waitFor(t, "updated def", func() bool {
		hc, ok := node.Registry().Chart("system.ram")
		if !ok || hc.Title != "Memory" {
			return false
		}
		for _, d := range hc.Dims() {
			if d.ID == "free" {
				return d.Name == "available" && d.Hidden
			}
		}
		return false
	})
	hc, _ := node.Registry().Chart("system.ram")
	if _, last := hc.LastValues(); last["used"] != 3 {
		t.Fatalf("last values after replace = %v", last)
	}

	// drop a dimension → hub drops it too
	reg.RemoveChart("system.ram")
	reg.AddChart(&registry.Chart{ID: "system.ram", Context: "system.ram", Title: "Memory", Units: "MiB", Dimensions: []*registry.Dimension{{ID: "used"}}})
	_ = reg.Collect("system.ram", now.Add(2*time.Second), map[string]float64{"used": 5})
	waitFor(t, "dimension removed", func() bool {
		hc, ok := node.Registry().Chart("system.ram")
		return ok && len(hc.Dims()) == 1
	})
}

func TestHubLiveUnknownNodeRejected(t *testing.T) {
	hubTS, _ := newHub(t, Options{})
	url := "ws" + strings.TrimPrefix(hubTS.URL, "http") + "/api/v1/live?node=nope"
	if _, resp, err := websocket.DefaultDialer.Dial(url, nil); err == nil || resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown node live: err=%v resp=%v", err, resp)
	}
}

func TestHubACLKEndToEnd(t *testing.T) {
	hubTS, nodes := newHub(t, Options{})
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{ID: "agent-mqtt", Hostname: "agent-mqtt", OS: "linux", UpdateEvery: 1}, db)
	reg.AddChart(&registry.Chart{ID: "system.ram", Context: "system.ram", Family: "ram", Title: "RAM", Units: "MiB",
		Type: registry.Stacked, Priority: 200, Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "free"}}})
	now := time.Now().Truncate(time.Second)
	_ = reg.Collect("system.ram", now.Add(-time.Second), map[string]float64{"used": 11, "free": 22})
	client := stream.NewClient(reg, db, stream.ClientOptions{
		Destinations: []string{hubTS.URL}, APIKey: testKey, Version: "t", Timeout: 5 * time.Second, Protocol: "mqtt",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go client.Run(ctx)

	waitFor(t, "aclk node online", func() bool {
		n, ok := nodes.Get("agent-mqtt")
		return ok && n.Online()
	})
	node, _ := nodes.Get("agent-mqtt")
	if inf := node.Info(time.Now()); inf.Protocol != "mqtt" || !inf.ACLK {
		t.Fatalf("info = %+v", inf)
	}
	_ = reg.Collect("system.ram", now.Add(time.Second), map[string]float64{"used": 33, "free": 22})
	waitFor(t, "aclk live sample", func() bool {
		_, last, ok := node.DB().Bounds(registry.SeriesID("system.ram", "used"))
		return ok && last >= now.Unix()
	})

	var info struct {
		ACLK struct {
			Available bool   `json:"available"`
			Protocol  string `json:"protocol"`
			Storage   string `json:"storage"`
		} `json:"aclk"`
	}
	getJSON(t, hubTS.URL+"/api/v1/info", &info)
	if !info.ACLK.Available || info.ACLK.Protocol != "stream+mqtt" || info.ACLK.Storage != "full" {
		t.Fatalf("hub info.aclk = %+v", info.ACLK)
	}

	raw, err := node.Query(context.Background(), map[string]string{"chart": "system.ram", "after": "-60"})
	if err != nil || len(raw) == 0 {
		t.Fatalf("query: %v %s", err, raw)
	}
}
