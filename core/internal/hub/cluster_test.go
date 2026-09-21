package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
