package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/stream"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func TestClusterFetchReadsPeerWithHop(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/alarms" || r.URL.Query().Get("node") != "agent-1" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get(clusterHopHeader) != "1" || r.Header.Get("Authorization") != "Bearer ptok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"alarms":{}}`))
	}))
	t.Cleanup(peer.Close)
	c := NewCluster([]string{peer.URL}, "ptok", nil)
	body, code, err := c.Fetch(context.Background(), peer.URL+"/", "/api/v1/alarms?node=agent-1")
	if err != nil || code != 200 || string(body) != `{"alarms":{}}` {
		t.Fatalf("body=%s code=%d err=%v", body, code, err)
	}
	if _, _, err := c.Fetch(context.Background(), peer.URL, "alarms"); err == nil {
		t.Fatal("relative path must be rejected")
	}
	if _, _, err := NewCluster(nil, "", nil).Fetch(context.Background(), peer.URL, "/api/v1/alarms"); err == nil {
		t.Fatal("empty cluster must be rejected")
	}
}

func TestClusterDiscoversPeerNodes(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/nodes" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get(clusterHopHeader) == "" {
			t.Errorf("missing hop header")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []Info{
			{ID: "", Hostname: "peer-hub", Local: true, Status: StatusLive},
			{ID: "agent-1", Hostname: "box", Status: StatusLive, ChartsCount: 3},
		}})
	}))
	defer peer.Close()
	c := NewCluster([]string{peer.URL}, "", nil)
	c.refresh()
	if !c.Has("agent-1") {
		t.Fatal("expected agent-1")
	}
	infos := c.PeerInfos()
	if len(infos) != 1 || infos[0].Hostname != "box" || infos[0].Peer != peer.URL {
		t.Fatalf("%+v", infos)
	}
}

func TestClusterRingPush(t *testing.T) {
	got := make(chan []byte, 1)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []Info{}})
		case "/api/v1/hub/ring":
			if r.Header.Get("Authorization") != "Bearer ptok" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			b, _ := io.ReadAll(r.Body)
			select {
			case got <- b:
			default:
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(peer.Close)

	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	n, err := Open(db, t.TempDir(), Options{Keys: []string{"k"}})
	if err != nil {
		t.Fatal(err)
	}
	node := n.newNode("box", registry.Host{ID: "box", Hostname: "box", OS: "linux", UpdateEvery: 1})
	n.mu.Lock()
	n.nodes["box"] = node
	n.mu.Unlock()
	def := &stream.ChartDef{ID: "system.ram", Context: "system.ram", Units: "MiB",
		Dimensions: []stream.DimDef{{ID: "used"}, {ID: "free"}}}
	node.reg.AddChart(def.ToChart())
	if err := node.reg.Ingest("system.ram", time.Now().Unix(), map[string]float64{"used": 11, "free": 22}); err != nil {
		t.Fatal(err)
	}

	c := NewCluster([]string{peer.URL}, "ptok", nil)
	c.SetNodes(n)
	c.refresh()
	select {
	case b := <-got:
		var p RingPayload
		if err := json.Unmarshal(b, &p); err != nil {
			t.Fatal(err)
		}
		if p.Host.Hostname != "box" || len(p.Samples) == 0 || p.Samples[0].V["used"] != 11 {
			t.Fatalf("ring payload = %+v", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("peer did not receive ring push")
	}

	node.mu.Lock()
	node.replica = true
	node.mu.Unlock()
	c.refresh()
	select {
	case b := <-got:
		t.Fatalf("replica node was pushed: %s", b)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestRingBackfillsAllSamplesAfterRejection(t *testing.T) {
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	nodes, err := Open(db, t.TempDir(), Options{Keys: []string{"key"}})
	if err != nil {
		t.Fatal(err)
	}
	node := nodes.newNode("box", registry.Host{ID: "box", UpdateEvery: 1})
	nodes.nodes["box"] = node
	ch := node.reg.AddChart(&registry.Chart{ID: "counter", Dimensions: []*registry.Dimension{{ID: "rate", Algorithm: registry.Incremental}}})
	start := time.Now().Unix() - 20
	for i := int64(0); i < 20; i++ {
		node.reg.Ingest(ch.ID, start+i, map[string]float64{"rate": float64(i + 1)})
	}
	var attempts int
	var received RingPayload
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			http.Error(w, "unavailable", 503)
			return
		}
		json.NewDecoder(r.Body).Decode(&received)
	}))
	defer peer.Close()
	c := NewCluster([]string{peer.URL}, "", nil)
	c.SetNodes(nodes)
	c.pushRing()
	if len(c.ringCursor) != 0 {
		t.Fatal("rejected page advanced cursor")
	}
	c.pushRing()
	if len(received.Samples) != 20 {
		t.Fatalf("lost history: %d", len(received.Samples))
	}
	replicaDB, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer replicaDB.Close()
	replicas, err := Open(replicaDB, t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	copied, err := replicas.AcceptReplica(received.Host, []*registry.Chart{received.Charts[0].ToChart()}, received.Samples, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pts, err := copied.db.QueryTier(registry.SeriesID("counter", "rate"), 0, start, start+19)
	if err != nil || len(pts) != 20 {
		t.Fatalf("replica points %d %v", len(pts), err)
	}
	for i, p := range pts {
		if p.Last != float64(i+1) {
			t.Fatalf("normalized rate transformed twice: %v", p)
		}
	}
}
