package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/stream"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

// ---------------------------------------------------------------------------
// Org: SetConfig / SetReport / SetApply merge semantics
// ---------------------------------------------------------------------------

// TestOrgConfigMergeSemantics walks the full lifecycle: desired state first
// (SetConfig), then the agent report (SetReport), then the apply ack
// (SetApply). Each writer must preserve the fields owned by the other two,
// and a later SetConfig must replace Disabled/YAML/Updated without clearing
// the reported file or the last apply outcome.
func TestOrgConfigMergeSemantics(t *testing.T) {
	o, err := OpenOrg(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := int64(1000)
	o.now = func() time.Time { return time.Unix(now, 0) } // deterministic Updated stamps

	if _, err := o.SetConfig(NodeConfig{NodeID: "", YAML: "x"}); err == nil {
		t.Fatal("SetConfig without node_id should fail")
	}
	if err := o.SetReport("", "y", 1); err == nil {
		t.Fatal("SetReport without node_id should fail")
	}
	if err := o.SetApply("", ApplyState{}); err == nil {
		t.Fatal("SetApply without node_id should fail")
	}

	// 1) desired state only
	got, err := o.SetConfig(NodeConfig{NodeID: "n1", Disabled: []string{"nvidia", "zfs"}, YAML: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Disabled) != 2 || got.Disabled[0] != "nvidia" || got.Disabled[1] != "zfs" {
		t.Fatalf("disabled = %v", got.Disabled)
	}
	if got.YAML != "v1" || got.Updated != 1000 {
		t.Fatalf("desired = yaml %q rev %d", got.YAML, got.Updated)
	}
	if got.Reported != "" || got.ReportAt != 0 || got.Apply != nil {
		t.Fatalf("fresh config should carry no report/apply: %+v", got)
	}

	// 2) agent report must preserve the desired state
	if err := o.SetReport("n1", "agent file v0\n", 1234); err != nil {
		t.Fatal(err)
	}
	cfg, ok := o.GetConfig("n1")
	if !ok {
		t.Fatal("config missing after SetReport")
	}
	if len(cfg.Disabled) != 2 || cfg.YAML != "v1" || cfg.Updated != 1000 {
		t.Fatalf("SetReport clobbered desired state: %+v", cfg)
	}
	if cfg.Reported != "agent file v0\n" || cfg.ReportAt != 1234 {
		t.Fatalf("report = %+v", cfg)
	}

	// 3) apply ack must preserve both desired state and the report
	if err := o.SetApply("n1", ApplyState{Rev: 1000, State: "applied", At: 1235}); err != nil {
		t.Fatal(err)
	}
	cfg, _ = o.GetConfig("n1")
	if len(cfg.Disabled) != 2 || cfg.YAML != "v1" || cfg.Updated != 1000 {
		t.Fatalf("SetApply clobbered desired state: %+v", cfg)
	}
	if cfg.Reported != "agent file v0\n" || cfg.ReportAt != 1234 {
		t.Fatalf("SetApply clobbered report: %+v", cfg)
	}
	if cfg.Apply == nil || cfg.Apply.Rev != 1000 || cfg.Apply.State != "applied" || cfg.Apply.At != 1235 {
		t.Fatalf("apply = %+v", cfg.Apply)
	}

	// 4) overwriting the YAML refreshes desired only: reported + apply stay
	now = 2000
	got, err = o.SetConfig(NodeConfig{NodeID: "n1", Disabled: []string{"ipmi"}, YAML: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	if got.YAML != "v2" || got.Updated != 2000 {
		t.Fatalf("overwrite = yaml %q rev %d", got.YAML, got.Updated)
	}
	if len(got.Disabled) != 1 || got.Disabled[0] != "ipmi" {
		t.Fatalf("disabled should be replaced: %v", got.Disabled)
	}
	if got.Reported != "agent file v0\n" || got.ReportAt != 1234 {
		t.Fatalf("overwrite cleared report: %+v", got)
	}
	if got.Apply == nil || got.Apply.State != "applied" {
		t.Fatalf("overwrite cleared apply: %+v", got.Apply)
	}

	// 5) an empty overlay clears Disabled but still keeps report + apply
	got, err = o.SetConfig(NodeConfig{NodeID: "n1", YAML: "v3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Disabled) != 0 {
		t.Fatalf("disabled should be empty: %v", got.Disabled)
	}
	if got.Reported == "" || got.Apply == nil {
		t.Fatalf("report/apply lost on empty overlay: %+v", got)
	}
}

// TestOrgConfigGetReturnsDeepCopy mutates the values handed out by GetConfig
// and ConfigByKey (slice element + pointed-to ApplyState) and verifies the
// stored state is unaffected.
func TestOrgConfigGetReturnsDeepCopy(t *testing.T) {
	o, err := OpenOrg(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.SetConfig(NodeConfig{NodeID: "n1", Disabled: []string{"nvidia"}, YAML: "v1"}); err != nil {
		t.Fatal(err)
	}
	if err := o.SetReport("n1", "reported", 111); err != nil {
		t.Fatal(err)
	}
	if err := o.SetApply("n1", ApplyState{Rev: 1, State: "applied", At: 222}); err != nil {
		t.Fatal(err)
	}

	first, ok := o.GetConfig("n1")
	if !ok {
		t.Fatal("config missing")
	}
	first.Disabled[0] = "mutated"
	first.YAML = "mutated"
	first.Apply.State = "rejected"
	first.Apply.Rev = 99

	again, _ := o.GetConfig("n1")
	if len(again.Disabled) != 1 || again.Disabled[0] != "nvidia" {
		t.Fatalf("Disabled slice aliases storage: %v", again.Disabled)
	}
	if again.YAML != "v1" {
		t.Fatalf("YAML aliased: %q", again.YAML)
	}
	if again.Apply == nil || again.Apply.State != "applied" || again.Apply.Rev != 1 {
		t.Fatalf("Apply aliases storage: %+v", again.Apply)
	}

	// same guarantee for the key-based lookup used on the ingest path
	sp, err := o.CreateSpace("prod")
	if err != nil {
		t.Fatal(err)
	}
	rm, err := o.CreateRoom(sp.ID, "edge")
	if err != nil {
		t.Fatal(err)
	}
	cl, err := o.IssueClaim(sp.ID, rm.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := o.RedeemClaim(cl.Token, "n1")
	if err != nil {
		t.Fatal(err)
	}
	byKey, ok := o.ConfigByKey(claimed.APIKey)
	if !ok || byKey.NodeID != "n1" {
		t.Fatalf("ConfigByKey = %+v %v", byKey, ok)
	}
	byKey.Disabled[0] = "mutated"
	byKey.Apply.State = "rejected"

	stored, _ := o.GetConfig("n1")
	if stored.Disabled[0] != "nvidia" || stored.Apply.State != "applied" {
		t.Fatalf("ConfigByKey leaked internal state: %+v", stored)
	}
}

// TestOrgConfigPersistenceRoundTrip saves via the ordinary Set* writers and
// reopens the org, expecting every config field back.
func TestOrgConfigPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	o, err := OpenOrg(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := int64(5000)
	o.now = func() time.Time { return time.Unix(now, 0) }

	if _, err := o.SetConfig(NodeConfig{NodeID: "n1", Disabled: []string{"nvidia", "zfs"}, YAML: "full: config\n"}); err != nil {
		t.Fatal(err)
	}
	if err := o.SetReport("n1", "agent live file", 5555); err != nil {
		t.Fatal(err)
	}
	if err := o.SetApply("n1", ApplyState{Rev: 5000, State: "rejected", Error: "invalid yaml", At: 6666}); err != nil {
		t.Fatal(err)
	}

	o2, err := OpenOrg(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, ok := o2.GetConfig("n1")
	if !ok {
		t.Fatal("config not restored")
	}
	if cfg.NodeID != "n1" {
		t.Fatalf("node id = %q", cfg.NodeID)
	}
	if len(cfg.Disabled) != 2 || cfg.Disabled[0] != "nvidia" || cfg.Disabled[1] != "zfs" {
		t.Fatalf("disabled = %v", cfg.Disabled)
	}
	if cfg.YAML != "full: config\n" || cfg.Updated != 5000 {
		t.Fatalf("desired = yaml %q rev %d", cfg.YAML, cfg.Updated)
	}
	if cfg.Reported != "agent live file" || cfg.ReportAt != 5555 {
		t.Fatalf("report = %+v", cfg)
	}
	if cfg.Apply == nil || cfg.Apply.Rev != 5000 || cfg.Apply.State != "rejected" ||
		cfg.Apply.Error != "invalid yaml" || cfg.Apply.At != 6666 {
		t.Fatalf("apply = %+v", cfg.Apply)
	}
}

// ---------------------------------------------------------------------------
// hub ingest: TypeConfig push on connect and TypeConfigState forwarding
// ---------------------------------------------------------------------------

// configTestHub is a hub serving the stream endpoint over a real websocket.
type configTestHub struct {
	*Nodes
	url string
}

// newConfigTestHub starts an ingest-enabled hub on an httptest server.
func newConfigTestHub(t *testing.T, opt Options) *configTestHub {
	t.Helper()
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	n, err := Open(db, t.TempDir(), opt)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(n.HandleStream))
	t.Cleanup(srv.Close)
	return &configTestHub{Nodes: n, url: srv.URL}
}

// connectAgent dials /api/v1/stream, sends hello and consumes the welcome.
func connectAgent(t *testing.T, h *configTestHub, id string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(h.url, "http") + stream.Path
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Authorization": []string{"Bearer k"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	sendFrame(t, ws, stream.Frame{Type: stream.TypeHello,
		Host: &registry.Host{ID: id, Hostname: id, OS: "linux", UpdateEvery: 1}})
	if f := readFrame(t, ws); f.Type != stream.TypeWelcome {
		t.Fatalf("expected welcome, got %+v", f)
	}
	return ws
}

func sendFrame(t *testing.T, ws *websocket.Conn, f stream.Frame) {
	t.Helper()
	if err := ws.WriteJSON(f); err != nil {
		t.Fatal(err)
	}
}

func readFrame(t *testing.T, ws *websocket.Conn) stream.Frame {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	var f stream.Frame
	if err := ws.ReadJSON(&f); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestOptionsConfigForNil covers both "no callback" and "callback returns
// nil": configFor must report nothing to push.
func TestOptionsConfigForNil(t *testing.T) {
	var o Options
	if c := o.configFor("x"); c != nil {
		t.Fatalf("no callback: %+v", c)
	}
	o.NodeConfig = func(string) *NodeConfig { return nil }
	if c := o.configFor("x"); c != nil {
		t.Fatalf("nil result: %+v", c)
	}
}

// TestIngestPushesConfigAfterWelcome checks that a node with a desired
// overlay receives a TypeConfig frame right after welcome, and that
// PushConfig on the now-online node queues a fresh revision.
func TestIngestPushesConfigAfterWelcome(t *testing.T) {
	asked := make(chan string, 4)
	want := &NodeConfig{NodeID: "agent-1", Disabled: []string{"nvidia", "zfs"}, YAML: "loglevel: info\n", Updated: 4242}
	h := newConfigTestHub(t, Options{Keys: []string{"k"}, NodeConfig: func(id string) *NodeConfig {
		asked <- id
		return want
	}})

	ws := connectAgent(t, h, "agent-1")
	select {
	case id := <-asked:
		if id != "agent-1" {
			t.Fatalf("NodeConfig asked for %q", id)
		}
	default:
	}

	f := readFrame(t, ws)
	if f.Type != stream.TypeConfig {
		t.Fatalf("expected config frame, got %+v", f)
	}
	if len(f.Disabled) != 2 || f.Disabled[0] != "nvidia" || f.Disabled[1] != "zfs" {
		t.Fatalf("disabled = %v", f.Disabled)
	}
	if f.ConfigYAML != "loglevel: info\n" || f.ConfigRev != 4242 {
		t.Fatalf("config = yaml %q rev %d", f.ConfigYAML, f.ConfigRev)
	}

	// node is online: a direct push must queue another TypeConfig frame
	nd, ok := h.Get("agent-1")
	if !ok {
		t.Fatal("node not registered")
	}
	if !nd.Online() {
		t.Fatal("node should be online")
	}
	if !nd.PushConfig(NodeConfig{NodeID: "agent-1", YAML: "loglevel: debug\n", Updated: 4243}) {
		t.Fatal("PushConfig on an online node returned false")
	}
	f = readFrame(t, ws)
	if f.Type != stream.TypeConfig || f.ConfigYAML != "loglevel: debug\n" || f.ConfigRev != 4243 {
		t.Fatalf("pushed frame = %+v", f)
	}
	if len(f.Disabled) != 0 {
		t.Fatalf("push carried unexpected overlay: %v", f.Disabled)
	}
}

// TestIngestNoConfigFrameWhenNothingDesired covers the two guards around the
// post-welcome push: a nil overlay and an overlay with neither disabled
// collectors nor YAML must not produce a frame.
func TestIngestNoConfigFrameWhenNothingDesired(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  func(string) *NodeConfig
	}{
		{"nil overlay", func(string) *NodeConfig { return nil }},
		{"empty overlay", func(string) *NodeConfig { return &NodeConfig{NodeID: "agent-2"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newConfigTestHub(t, Options{Keys: []string{"k"}, NodeConfig: tc.cfg})
			ws := connectAgent(t, h, "agent-2")
			_ = ws.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
			var f stream.Frame
			if err := ws.ReadJSON(&f); err == nil {
				t.Fatalf("unexpected frame after welcome: %+v", f)
			}
		})
	}
}

// TestIngestConfigStateForwardedToCallback sends config_state frames from the
// agent side and expects OnConfigState to receive the node id together with
// the frame payload (file report and apply ack shapes).
func TestIngestConfigStateForwardedToCallback(t *testing.T) {
	type report struct {
		node string
		f    stream.Frame
	}
	got := make(chan report, 2)
	h := newConfigTestHub(t, Options{Keys: []string{"k"},
		OnConfigState: func(id string, f stream.Frame) { got <- report{id, f} }})

	ws := connectAgent(t, h, "agent-9")

	// live config file report
	sendFrame(t, ws, stream.Frame{Type: stream.TypeConfigState,
		ConfigYAML: "live file contents\n", ConfigPath: "/etc/monitor/monitor.yaml"})
	select {
	case r := <-got:
		if r.node != "agent-9" {
			t.Fatalf("callback node = %q", r.node)
		}
		if r.f.Type != stream.TypeConfigState || r.f.ConfigYAML != "live file contents\n" ||
			r.f.ConfigPath != "/etc/monitor/monitor.yaml" {
			t.Fatalf("report frame = %+v", r.f)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OnConfigState not called for file report")
	}

	// apply outcome ack
	sendFrame(t, ws, stream.Frame{Type: stream.TypeConfigState,
		ConfigRev: 77, ApplyState: "rejected", ApplyError: "invalid section"})
	select {
	case r := <-got:
		if r.node != "agent-9" {
			t.Fatalf("callback node = %q", r.node)
		}
		if r.f.ConfigRev != 77 || r.f.ApplyState != "rejected" || r.f.ApplyError != "invalid section" {
			t.Fatalf("ack frame = %+v", r.f)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OnConfigState not called for apply ack")
	}
}

// TestNodePushConfigOffline: a registered node with no live connection must
// refuse the push.
func TestNodePushConfigOffline(t *testing.T) {
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n, err := Open(db, t.TempDir(), Options{Keys: []string{"k"}})
	if err != nil {
		t.Fatal(err)
	}
	ghost := n.newNode("ghost", registry.Host{ID: "ghost", Hostname: "ghost", UpdateEvery: 1})
	n.mu.Lock()
	n.nodes["ghost"] = ghost
	n.gen++
	n.mu.Unlock()
	if ghost.Online() {
		t.Fatal("node should be offline")
	}
	if ghost.PushConfig(NodeConfig{NodeID: "ghost", YAML: "x", Updated: 1}) {
		t.Fatal("PushConfig on an offline node returned true")
	}
}
