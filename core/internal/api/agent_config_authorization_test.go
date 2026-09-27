package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mengzhihua/monitor/core/internal/hub"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/stream"
)

func TestAgentConfigRequiresNodeKeyBinding(t *testing.T) {
	hs, _, org, _ := newHubCfg(t, Options{})
	spaces := org.Spaces()
	rooms := org.Rooms(spaces[0].ID)
	claim, err := org.IssueClaim(spaces[0].ID, rooms[0].ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := org.RedeemClaim(claim.Token, "claimed-node")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"shared-a", "shared-b", "claimed-node", "not-registered"} {
		if _, err := org.SetConfig(hub.NodeConfig{NodeID: id, YAML: "web:\n  token: secret-for-" + id + "\n"}); err != nil {
			t.Fatal(err)
		}
		if err := org.SetReport(id, "web:\n  token: reported-secret\n", 1); err != nil {
			t.Fatal(err)
		}
	}
	// Two distinct agents may intentionally share a static stream credential.
	for _, id := range []string{"shared-a", "shared-b"} {
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(hs.URL, "http")+stream.Path,
			http.Header{"Authorization": []string{"Bearer " + testKey}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if err := conn.WriteJSON(stream.Frame{Type: stream.TypeHello, Host: &registry.Host{ID: id, Hostname: id, UpdateEvery: 1}}); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var welcome stream.Frame
		if err := conn.ReadJSON(&welcome); err != nil || welcome.Type != stream.TypeWelcome {
			t.Fatalf("register %s: frame=%+v err=%v", id, welcome, err)
		}
	}
	for _, tc := range []struct {
		name, key, node, wantNode string
		status                    int
	}{
		{"shared first", testKey, "shared-a", "shared-a", http.StatusOK},
		{"shared second", testKey, "shared-b", "shared-b", http.StatusOK},
		{"claim before stream", claimed.APIKey, "", "claimed-node", http.StatusOK},
		{"claim explicit own", claimed.APIKey, "claimed-node", "claimed-node", http.StatusOK},
		{"static reads other key", testKey, "claimed-node", "", http.StatusForbidden},
		{"claim reads static", claimed.APIKey, "shared-a", "", http.StatusForbidden},
		{"unknown preconfigured", testKey, "not-registered", "", http.StatusForbidden},
		{"unknown", testKey, "unknown", "", http.StatusForbidden},
		{"legacy no node", testKey, "", "", http.StatusOK},
		{"invalid key", "invalid-key", "shared-a", "", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, hs.URL+"/api/v1/agent/config?node="+tc.node, nil)
			req.Header.Set("Authorization", "Bearer "+tc.key)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != tc.status || resp.Header.Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d want=%d cache=%q", resp.StatusCode, tc.status, resp.Header.Get("Cache-Control"))
			}
			if tc.status == http.StatusOK {
				var cfg hub.NodeConfig
				if err := json.Unmarshal(raw, &cfg); err != nil || cfg.NodeID != tc.wantNode {
					t.Fatalf("node=%q want=%q err=%v", cfg.NodeID, tc.wantNode, err)
				}
				if tc.wantNode != "" && (!strings.Contains(cfg.YAML, "secret-for-"+tc.wantNode) || cfg.Reported == "") {
					t.Fatal("authorized node lost its config")
				}
			} else if strings.Contains(string(raw), "secret") {
				t.Fatal("rejected request leaked config")
			}
		})
	}
}

func TestClaimCannotOverrideExistingNodeBinding(t *testing.T) {
	hs, _, org, _ := newHubCfg(t, Options{})
	const nodeID = "registered-victim"
	if _, err := org.SetConfig(hub.NodeConfig{NodeID: nodeID, YAML: "web:\n  token: victim-secret\n"}); err != nil {
		t.Fatal(err)
	}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(hs.URL, "http")+stream.Path,
		http.Header{"Authorization": []string{"Bearer " + testKey}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.WriteJSON(stream.Frame{Type: stream.TypeHello, Host: &registry.Host{ID: nodeID, Hostname: nodeID, UpdateEvery: 1}}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var welcome stream.Frame
	if err := conn.ReadJSON(&welcome); err != nil || welcome.Type != stream.TypeWelcome {
		t.Fatalf("node registration failed: %v", err)
	}
	spaces := org.Spaces()
	rooms := org.Rooms(spaces[0].ID)
	claim, err := org.IssueClaim(spaces[0].ID, rooms[0].ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"token": claim.Token, "node_id": nodeID})
	resp, err := http.Post(hs.URL+"/api/v1/claim", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || len(org.Keys()) != 0 {
		t.Fatalf("claim of bound node status=%d accepted keys=%d", resp.StatusCode, len(org.Keys()))
	}
	for _, saved := range org.Claims() {
		if saved.Token == claim.Token && (saved.UsedAt != 0 || saved.NodeID != "") {
			t.Fatal("rejected claim was consumed")
		}
	}
	// Model a conflicting claim already minted by an older server: the read
	// path must still refuse it, even though ConfigByKey resolves the victim.
	legacy, err := org.RedeemClaim(claim.Token, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"", "?node=" + nodeID} {
		req, _ := http.NewRequest(http.MethodGet, hs.URL+"/api/v1/agent/config"+query, nil)
		req.Header.Set("Authorization", "Bearer "+legacy.APIKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || strings.Contains(string(raw), "victim-secret") {
			t.Fatalf("conflicting legacy claim read status=%d", resp.StatusCode)
		}
	}
}
