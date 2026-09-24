package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/hub"
	"github.com/mengzhihua/monitor/core/internal/operations"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/stream"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func TestOperationsMetricsAndCoverage(t *testing.T) {
	ts, reg := newTestServer(t, Options{Token: "secret"})
	if resp := getJSON(t, ts.URL+"/api/v1/operations", nil); resp.StatusCode != 401 {
		t.Fatal(resp.StatusCode)
	}
	var snap operationsSnapshot
	getJSON(t, ts.URL+"/api/v1/operations?token=secret&node=unknown&status=offline", &snap)
	if len(snap.Nodes) != 1 || snap.Nodes[0].Memory.Value != nil || snap.Nodes[0].AlarmCoverage != "disabled" {
		t.Fatalf("%+v", snap)
	}
	now := time.Now().Truncate(time.Second)
	_ = reg.Collect("system.ram", now, map[string]float64{"used": 75, "free": 25})
	getJSON(t, ts.URL+"/api/v1/operations?token=secret", &snap)
	if got := snap.Nodes[0].Memory; got.Value == nil || *got.Value != 75 || got.State != "fresh" {
		t.Fatal(got)
	}
	for _, status := range []string{"offline", "stale"} {
		if got := metricFor(reg, "system.ram", status, now.Unix()); got.Value != nil || got.State != "stale" {
			t.Fatal(got)
		}
	}
	if got := metricFor(reg, "system.ram", "live", now.Unix()+60); got.Value != nil || got.State != "stale" {
		t.Fatal(got)
	}
	_ = reg.Collect("system.ram", now.Add(time.Second), map[string]float64{"used": math.NaN(), "free": 25})
	getJSON(t, ts.URL+"/api/v1/operations?token=secret", &snap)
	if snap.Nodes[0].Memory.Value != nil {
		t.Fatal("invalid metric displayed as real value")
	}
	reg.AddChart(&registry.Chart{ID: "system.cpu", Dimensions: []*registry.Dimension{{ID: "user"}, {ID: "idle"}}})
	_ = reg.Collect("system.cpu", now, map[string]float64{"user": 25, "idle": 75})
	if got := metricFor(reg, "system.cpu", "live", now.Unix()); got.Value == nil || *got.Value != 25 {
		t.Fatal(got)
	}
	_ = reg.Collect("system.cpu", now.Add(time.Second), map[string]float64{"user": 40})
	if got := metricFor(reg, "system.cpu", "live", now.Unix()+1); got.Value != nil {
		t.Fatal("partial CPU snapshot reported a percentage")
	}
}

func TestOperationsIncludesPeerAlarms(t *testing.T) {
	now := time.Now().Unix()
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/alarms" && r.Header.Get("X-Monitor-Cluster-Hop") != "" {
			http.Error(w, "direct peer must be allowed to proxy once", http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/api/v1/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []hub.Info{
				{ID: "agent-1", Hostname: "box", Status: hub.StatusLive},
				{ID: "agent-2", Hostname: "down", Status: hub.StatusLive},
			}})
		case "/api/v1/alarms":
			if r.URL.Query().Get("node") == "agent-2" {
				http.Error(w, "unavailable", http.StatusBadGateway)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"alarms": map[string]health.Alarm{
				"disk|disk.space": {Name: "disk", Chart: "disk.space", Family: "disk", Status: health.StatusCritical, Info: "full", LastStatusChange: now, LastUpdated: now, Every: 10},
				"ok|system.cpu":   {Name: "ok", Chart: "system.cpu", Status: health.StatusClear, LastUpdated: now},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(peer.Close)
	cluster := hub.NewCluster([]string{peer.URL}, "", nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go cluster.Run(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !cluster.Has("agent-1") {
		time.Sleep(10 * time.Millisecond)
	}
	ts, _ := newTestServer(t, Options{Cluster: cluster})
	var snap operationsSnapshot
	getJSON(t, ts.URL+"/api/v1/operations", &snap)
	var box, down *operationsNode
	for i := range snap.Nodes {
		switch snap.Nodes[i].ID {
		case "agent-1":
			box = &snap.Nodes[i]
		case "agent-2":
			down = &snap.Nodes[i]
		}
	}
	if box == nil || box.AlarmCoverage != "peer" || down == nil || down.AlarmCoverage != "unknown" {
		t.Fatalf("nodes=%+v", snap.Nodes)
	}
	if snap.Summary["critical"] != 1 || snap.Summary["coverage_unknown"] < 1 || len(snap.Problems) != 1 || snap.Problems[0].Hostname != "box" || snap.Problems[0].Name != "disk" {
		t.Fatalf("summary=%v problems=%+v", snap.Summary, snap.Problems)
	}
}

func TestOperationsRelaysPeerAlarmsOneHop(t *testing.T) {
	now := time.Now().Unix()
	leaf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Monitor-Cluster-Hop") != "1" || r.URL.Query().Get("node") != "agent-9" {
			http.Error(w, "leaf expects one hop", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"alarms": map[string]health.Alarm{
			"disk|disk.space": {Name: "disk", Chart: "disk.space", Status: health.StatusCritical, LastStatusChange: now, LastUpdated: now, Every: 10},
		}})
	}))
	t.Cleanup(leaf.Close)
	mid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []hub.Info{{ID: "agent-9", Hostname: "far", Status: hub.StatusLive}}})
		case "/api/v1/alarms":
			if r.Header.Get("X-Monitor-Cluster-Hop") != "" {
				http.Error(w, "middle hop must stay open", http.StatusBadRequest)
				return
			}
			req, err := http.NewRequest(http.MethodGet, leaf.URL+r.URL.RequestURI(), nil)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			req.Header.Set("X-Monitor-Cluster-Hop", "1")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			defer resp.Body.Close()
			w.WriteHeader(resp.StatusCode)
			_, _ = io.Copy(w, resp.Body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(mid.Close)
	cluster := hub.NewCluster([]string{mid.URL}, "", nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go cluster.Run(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !cluster.Has("agent-9") {
		time.Sleep(10 * time.Millisecond)
	}
	ts, _ := newTestServer(t, Options{Cluster: cluster})
	var snap operationsSnapshot
	getJSON(t, ts.URL+"/api/v1/operations", &snap)
	var found bool
	for _, p := range snap.Problems {
		if p.Hostname == "far" && p.Name == "disk" {
			found = true
		}
	}
	if !found {
		t.Fatalf("problems=%+v", snap.Problems)
	}
}

func TestOperationsShowsLocalDeliveryAndPostsTicket(t *testing.T) {
	var got []byte
	ticket := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(ticket.Close)
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{ID: "local", Hostname: "ops-test", UpdateEvery: 1}, db)
	reg.AddChart(&registry.Chart{ID: "system.ram", Family: "ram", Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "free"}}})
	rules, err := health.ParseRules([]byte("alarms:\n  - name: ram_high\n    on: system.ram\n    calc: '$used'\n    every: 1s\n    warn: '$this > 50'\n"), "test")
	if err != nil {
		t.Fatal(err)
	}
	eng, err := health.New(reg, db, health.Options{Rules: rules, SilenceAll: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	now := time.Now().Truncate(time.Second)
	_ = reg.Collect("system.ram", now, map[string]float64{"used": 80, "free": 20})
	eng.Tick(now)
	ts, _ := newTestServer(t, Options{Health: eng, TicketWebhook: ticket.URL, Token: "secret"})
	var snap operationsSnapshot
	getJSON(t, ts.URL+"/api/v1/operations?token=secret", &snap)
	if len(snap.Problems) != 1 || snap.Problems[0].Delivery == nil || snap.Problems[0].Delivery.Outcome != "suppressed" {
		t.Fatalf("%+v", snap.Problems)
	}
	p := snap.Problems[0]
	b, _ := json.Marshal(map[string]any{"id": p.ID, "action": "acknowledge", "revision": p.Handling.Revision, "note": "ticket"})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/operations/acknowledgements?token=secret", bytes.NewReader(b))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Contains(got, []byte(`"event":"handling"`)) || !bytes.Contains(got, []byte(p.ID)) {
		t.Fatalf("status=%d body=%s", resp.StatusCode, got)
	}
}

func TestOperationsAcknowledgementLifecycleAndRBAC(t *testing.T) {
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reg := registry.New(&registry.Host{ID: "local", Hostname: "ops-test", UpdateEvery: 1}, db)
	reg.AddChart(&registry.Chart{ID: "system.ram", Family: "ram", Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "free"}}})
	rules, err := health.ParseRules([]byte(`alarms:
  - name: ram_high
    on: system.ram
    calc: '$used'
    every: 1s
    warn: '$this > 50'
    crit: '$this > 90'
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	eng, err := health.New(reg, db, health.Options{Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	sched := collect.NewScheduler(reg, nil, collect.Options{Names: []string{"none"}})
	opt := Options{Health: eng, OperationsDir: t.TempDir(), Users: []User{{Name: "reader", Token: "viewer", Role: RoleViewer}, {Name: "on-call", Token: "operator", Role: RoleTroubleshooter}, {Name: "backup", Token: "backup-token", Role: RoleAdmin}}}
	s, err := New(reg, db, sched, opt)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	now := time.Now().Truncate(time.Second)
	set := func(v float64, sec int) {
		_ = reg.Collect("system.ram", now.Add(time.Duration(sec)*time.Second), map[string]float64{"used": v, "free": 100 - v})
		eng.Tick(now.Add(time.Duration(sec) * time.Second))
	}
	set(80, 0)
	var snap operationsSnapshot
	getJSON(t, ts.URL+"/api/v1/operations?token=viewer", &snap)
	if len(snap.Problems) != 1 {
		t.Fatalf("%+v", snap)
	}
	p := snap.Problems[0]
	post := func(token, id, action string, revision uint64, want int) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"id": id, "action": action, "revision": revision, "note": "investigating"})
		req, _ := http.NewRequest("POST", ts.URL+"/api/v1/operations/acknowledgements", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("action=%s status=%d want=%d", action, resp.StatusCode, want)
		}
	}
	post("viewer", p.ID, "acknowledge", 0, 403)
	post("operator", p.ID, "acknowledge", 0, 200)
	post("operator", p.ID, "unacknowledge", 0, 409)
	getJSON(t, ts.URL+"/api/v1/operations?token=viewer", &snap)
	if snap.Summary["unacknowledged"] != 0 || snap.Problems[0].Handling.History[0].Actor != "on-call" {
		t.Fatal(snap)
	}
	if eng.Alarms()[0].Status != health.StatusWarning || eng.IsSilenced("system.ram", "ram_high") {
		t.Fatal("acknowledgement altered health evaluation")
	}
	if snap.CurrentUser.Name != "reader" || len(snap.Assignees) != 2 || snap.Assignees[0].Name != "backup" || snap.Assignees[1].Name != "on-call" {
		t.Fatal(snap.Assignees, snap.CurrentUser)
	}
	b, _ := json.Marshal(snap)
	if bytes.Contains(b, []byte("backup-token")) || bytes.Contains(b, []byte(`"token"`)) {
		t.Fatal("snapshot exposed credential")
	}
	workflow := func(token string, revision uint64, change map[string]any, want int) {
		t.Helper()
		change["id"], change["revision"] = p.ID, revision
		b, _ := json.Marshal(change)
		req, _ := http.NewRequest("POST", ts.URL+"/api/v1/operations/handling", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("workflow %v status=%d want=%d: %s", change, resp.StatusCode, want, body)
		}
	}
	workflow("viewer", 1, map[string]any{"action": "assign", "assignee": "on-call"}, 403)
	workflow("", 1, map[string]any{"action": "assign", "assignee": "on-call"}, 401)
	for _, name := range []string{"reader", "not-configured"} {
		workflow("operator", 1, map[string]any{"action": "assign", "assignee": name}, 400)
	}
	workflow("operator", 1, map[string]any{"action": "progress", "status": "resolved"}, 400)
	workflow("operator", 1, map[string]any{"action": "assign", "assignee": "backup", "actor": "forged"}, 400)
	post("operator", p.ID, "assign", 1, 400)
	workflow("operator", 1, map[string]any{"action": "assign", "assignee": "on-call"}, 200)
	workflow("backup-token", 1, map[string]any{"action": "assign", "assignee": "backup"}, 409)
	workflow("operator", 2, map[string]any{"action": "progress", "status": "investigating", "note": "checking memory"}, 200)
	getJSON(t, ts.URL+"/api/v1/operations?token=viewer", &snap)
	if snap.Summary["unassigned"] != 0 || snap.Summary["investigating"] != 1 || snap.Problems[0].Handling.Assignee != "on-call" || snap.Problems[0].Handling.Status != operations.StatusInvestigating || !snap.Problems[0].Handling.Acknowledged {
		t.Fatal(snap)
	}
	if eng.Alarms()[0].Status != health.StatusWarning || eng.IsSilenced("system.ram", "ram_high") {
		t.Fatal("workflow altered alarm state")
	}
	// New server with the same state directory keeps the acknowledgement.
	s2, err := New(reg, db, sched, opt)
	if err != nil {
		t.Fatal(err)
	}
	if r := s2.operations.Get(p.ID); !r.Acknowledged || r.Assignee != "on-call" || r.Status != operations.StatusInvestigating {
		t.Fatal("restart lost record")
	}
	set(95, 2)
	getJSON(t, ts.URL+"/api/v1/operations?token=viewer", &snap)
	if snap.Problems[0].ID == p.ID || snap.Problems[0].Handling.Acknowledged || snap.Problems[0].Severity != "CRITICAL" || snap.Problems[0].Handling.Assignee != "" || snap.Problems[0].Handling.Status != operations.StatusOpen {
		t.Fatal("escalation inherited acknowledgement")
	}
	post("operator", p.ID, "acknowledge", 1, 409)
	workflow("operator", 3, map[string]any{"action": "progress", "status": "watching"}, 409)
	set(5, 4)
	getJSON(t, ts.URL+"/api/v1/operations?token=viewer", &snap)
	if len(snap.Problems) != 0 {
		t.Fatal("recovered problem remains active")
	}
	set(80, 6)
	getJSON(t, ts.URL+"/api/v1/operations?token=viewer", &snap)
	if len(snap.Problems) != 1 || snap.Problems[0].ID == p.ID || snap.Problems[0].Handling.Acknowledged {
		t.Fatal("retrigger inherited acknowledgement")
	}
	// A troubleshooter may handle incidents, but must not acquire unrelated mutation rights.
	r, _ := http.NewRequest("POST", ts.URL+"/api/v1/alarms/silence?token=operator", strings.NewReader(`{"all":true}`))
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal(resp.StatusCode)
	}
}

func TestOperationsHubOfflineProblemsRemainVisible(t *testing.T) {
	hubTS, nodes := newHub(t, Options{})
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reg := registry.New(&registry.Host{ID: "remote-ops", Hostname: "remote-ops", UpdateEvery: 1}, db)
	client := stream.NewClient(reg, db, stream.ClientOptions{Destinations: []string{hubTS.URL}, APIKey: testKey, Timeout: time.Second,
		Alarms: func() []health.Alarm {
			return []health.Alarm{{ID: 1, Chart: "system.ram", Name: "ram_high", Status: health.StatusCritical, LastStatusChange: time.Now().Unix() - 10, LastUpdated: time.Now().Unix()}}
		}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go client.Run(ctx)
	n := waitNode(t, nodes, "remote-ops")
	waitFor(t, "alarm snapshot", func() bool { return len(n.Alarms()) == 1 })
	cancel()
	waitFor(t, "offline", func() bool { return n.Status(time.Now()) == "offline" })
	var snap operationsSnapshot
	getJSON(t, hubTS.URL+"/api/v1/operations", &snap)
	if len(snap.Nodes) != 2 || len(snap.Problems) != 1 || !snap.Problems[0].Stale || snap.Problems[0].Node != "remote-ops" || snap.Summary["offline"] != 1 {
		t.Fatalf("%+v", snap)
	}
}

func TestOperationsPageKeepsGlobalSummary(t *testing.T) {
	snap := operationsSnapshot{
		CurrentUser: User{Name: "ada"},
		Summary:     map[string]int{"nodes": 3, "critical": 2},
		Nodes: []operationsNode{
			{Info: hub.Info{ID: "b", Hostname: "b", Status: "live"}},
			{Info: hub.Info{ID: "a", Hostname: "a", Status: "offline", Alarms: map[string]int{"critical": 2}}},
			{Info: hub.Info{ID: "c", Hostname: "c", Status: "live"}},
		},
		Problems: []problem{
			{ID: "1", Name: "cpu", Severity: "CRITICAL", NodeStatus: "live", Family: "cpu", Handling: operations.Record{Status: "open"}},
			{ID: "2", Name: "ram", Severity: "WARNING", NodeStatus: "live", Family: "ram", Handling: operations.Record{Status: "open", Acknowledged: true}},
			{ID: "3", Name: "disk", Severity: "WARNING", NodeStatus: "offline", Family: "disk", Handling: operations.Record{Assignee: "ada", Status: "investigating"}},
		},
	}
	req := httptest.NewRequest("GET", "/api/v1/operations?limit=1&severity=WARNING&owner=mine", nil)
	got := pageOperations(snap, req, 1)
	if got.Summary["nodes"] != 3 || got.Page == nil || got.Page.ProblemsMatched != 1 || len(got.Problems) != 1 || got.Problems[0].Name != "disk" {
		t.Fatalf("%+v page=%+v", got.Problems, got.Page)
	}
	if got.Page.NodesMatched != 3 || len(got.Nodes) != 1 || got.Nodes[0].ID != "a" {
		t.Fatalf("nodes=%+v matched=%d", got.Nodes, got.Page.NodesMatched)
	}
	if _, ok := operationsLimit(httptest.NewRequest("GET", "/api/v1/operations?limit=0", nil)); ok {
		t.Fatal("limit 0 is not a page size")
	}
}

func TestOperationsDiskMetrics(t *testing.T) {
	ts, reg := newTestServer(t, Options{Token: "secret"})
	// A node without disk charts must expose an empty array, not JSON null.
	resp, err := http.Get(ts.URL + "/api/v1/operations?token=secret")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `"disks":[]`) {
		t.Fatalf("expected empty disks array, got %s", body)
	}
	now := time.Now().Truncate(time.Second)
	add := func(id, mp string, dims ...string) {
		ds := make([]*registry.Dimension, 0, len(dims))
		for _, d := range dims {
			ds = append(ds, &registry.Dimension{ID: d})
		}
		reg.AddChart(&registry.Chart{ID: id, Context: "disk.space", Family: mp, Dimensions: ds})
	}
	add("disk_space.root", "/", "used", "avail")
	add("disk_space.data", "/data", "used", "avail")
	add("disk_space.backup", "/backup", "used", "avail", "reserved") // FreeBSD layout
	_ = reg.Collect("disk_space.root", now, map[string]float64{"used": 90, "avail": 10})
	_ = reg.Collect("disk_space.data", now, map[string]float64{"used": 25, "avail": 75})
	_ = reg.Collect("disk_space.backup", now, map[string]float64{"used": 40, "avail": 40, "reserved": 20})
	var snap operationsSnapshot
	getJSON(t, ts.URL+"/api/v1/operations?token=secret", &snap)
	disks := snap.Nodes[0].Disks
	if len(disks) != 3 {
		t.Fatalf("%+v", disks)
	}
	for i, w := range []struct {
		mount string
		value float64
	}{{"/", 90}, {"/backup", 40}, {"/data", 25}} {
		d := disks[i]
		if d.Mount != w.mount || d.Value == nil || *d.Value != w.value || d.State != "fresh" || d.At != now.Unix() {
			t.Fatalf("disk %d = %+v, want %s=%.0f fresh", i, d, w.mount, w.value)
		}
	}
	// Disks age on a 60s window: a 20s-old sample is still fresh (CPU would be stale at 15s).
	_ = reg.Collect("disk_space.data", now.Add(-20*time.Second), map[string]float64{"used": 25, "avail": 75})
	disks = diskMetricsFor(reg, "live", now.Unix())
	if d := disks[len(disks)-1]; d.Mount != "/data" || d.State != "fresh" || d.Value == nil || *d.Value != 25 {
		t.Fatalf("20s-old sample must stay fresh: %+v", d)
	}
	// 120s-old samples go stale, lose their value and sort behind fresh mounts by mount point.
	_ = reg.Collect("disk_space.data", now.Add(-120*time.Second), map[string]float64{"used": 25, "avail": 75})
	_ = reg.Collect("disk_space.backup", now.Add(-120*time.Second), map[string]float64{"used": 40, "avail": 40, "reserved": 20})
	disks = diskMetricsFor(reg, "live", now.Unix())
	if len(disks) != 3 || disks[0].Mount != "/" || disks[0].State != "fresh" || disks[1].Mount != "/backup" || disks[2].Mount != "/data" {
		t.Fatalf("%+v", disks)
	}
	for _, d := range disks[1:] {
		if d.State != "stale" || d.Value != nil {
			t.Fatalf("expected stale disk without value: %+v", d)
		}
	}
	if d := diskMetricsFor(reg, "offline", now.Unix())[0]; d.State != "stale" || d.Value != nil {
		t.Fatalf("offline node must report stale disks: %+v", d)
	}
	_ = reg.Collect("disk_space.root", now.Add(10*time.Second), map[string]float64{"used": 90, "avail": 10})
	if d := diskMetricsFor(reg, "live", now.Unix())[0]; d.State != "stale" || d.Value != nil {
		t.Fatalf("future sample must be stale: %+v", d)
	}
	_ = reg.Collect("disk_space.root", now, map[string]float64{"used": 90, "avail": 10})
	// Missing, negative or zero-sum dimensions leave the disk unavailable.
	add("disk_space.partial", "/partial", "used", "avail")
	_ = reg.Collect("disk_space.partial", now, map[string]float64{"avail": 50}) // used never reported
	add("disk_space.negative", "/negative", "used", "avail")
	_ = reg.Collect("disk_space.negative", now, map[string]float64{"used": 50, "avail": -10})
	add("disk_space.empty", "/empty", "used", "avail")
	_ = reg.Collect("disk_space.empty", now, map[string]float64{"used": 0, "avail": 0})
	disks = diskMetricsFor(reg, "live", now.Unix())
	if len(disks) != 6 || disks[0].Mount != "/" || disks[0].Value == nil || *disks[0].Value != 90 {
		t.Fatalf("%+v", disks)
	}
	for i, mount := range []string{"/backup", "/data", "/empty", "/negative", "/partial"} {
		if disks[i+1].Mount != mount || disks[i+1].Value != nil {
			t.Fatalf("unexpected order at %d: %+v", i+1, disks)
		}
	}
	for _, d := range disks[3:] {
		if d.State != "unavailable" {
			t.Fatalf("bad dimensions must be unavailable: %+v", d)
		}
	}
}
