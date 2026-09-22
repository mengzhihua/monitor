package hub

import (
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
