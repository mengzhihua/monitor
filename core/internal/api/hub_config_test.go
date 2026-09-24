package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/hub"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

// newHubCfg is newHubOrg with a configurable Options (auth users) and the org
// directory exposed so tests can reopen the store and verify persistence.
func newHubCfg(t *testing.T, extra Options) (*httptest.Server, *hub.Nodes, *hub.Org, string) {
	t.Helper()
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	orgDir := t.TempDir()
	org, err := hub.OpenOrg(orgDir)
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := org.CreateSpace("prod")
	_, _ = org.CreateRoom(sp.ID, "edge")
	var srv *Server
	nodes, err := hub.Open(db, t.TempDir(), hub.Options{Keys: []string{testKey}, ExtraKeys: org.Keys,
		OnSample: func(n, c string, ts int64, v map[string]float64) { srv.PublishNodeSample(n, c, ts, v) }})
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New(&registry.Host{ID: "hub-id", Hostname: "hub", OS: "linux", UpdateEvery: 1}, db)
	sched := collect.NewScheduler(reg, nil, collect.Options{Names: []string{"none"}})
	extra.Mode, extra.Nodes, extra.Org, extra.StartedAt = "hub", nodes, org, time.Now()
	srv, err = New(reg, db, sched, extra)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, nodes, org, orgDir
}

type hubConfigJSON struct {
	NodeID   string          `json:"node_id"`
	Disabled []string        `json:"disabled"`
	YAML     string          `json:"yaml"`
	Updated  int64           `json:"updated"`
	Reported string          `json:"reported"`
	Apply    *hub.ApplyState `json:"apply"`
	Online   bool            `json:"online"`
	Pushed   bool            `json:"pushed"`
	Pending  bool            `json:"pending"`
}

func putHubConfig(t *testing.T, url string, body map[string]any) (*http.Response, hubConfigJSON, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(b))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out hubConfigJSON
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
	}
	return resp, out, string(raw)
}

func getHubConfig(t *testing.T, url, token string) (*http.Response, hubConfigJSON) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out hubConfigJSON
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
	}
	return resp, out
}

func TestHubConfigYAML(t *testing.T) {
	hs, _, org, orgDir := newHubCfg(t, Options{})
	url := hs.URL + "/api/v1/hub/config?node=box-1"
	yamlCfg := "mode: agent\nglobal:\n  update_every: 2\n"

	// PUT yaml + disabled on an offline node
	resp, out, _ := putHubConfig(t, url, map[string]any{"yaml": yamlCfg, "disabled": []string{"nvidia"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put status = %d", resp.StatusCode)
	}
	if out.NodeID != "box-1" || out.YAML != yamlCfg || len(out.Disabled) != 1 || out.Disabled[0] != "nvidia" {
		t.Fatalf("put = %+v", out)
	}
	if out.Online || out.Pushed || !out.Pending {
		t.Fatalf("put state = %+v", out)
	}

	// GET reflects the stored config and reports pending until acked
	resp, got := getHubConfig(t, url, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d", resp.StatusCode)
	}
	if got.YAML != yamlCfg || !got.Pending || got.Online {
		t.Fatalf("get = %+v", got)
	}

	// persisted: reopening the org store keeps yaml and disabled
	org2, err := hub.OpenOrg(orgDir)
	if err != nil {
		t.Fatal(err)
	}
	saved, ok := org2.GetConfig("box-1")
	if !ok || saved.YAML != yamlCfg || len(saved.Disabled) != 1 {
		t.Fatalf("persisted = %+v ok=%v", saved, ok)
	}

	// invalid yaml -> 400, stored value unchanged
	resp, _, _ = putHubConfig(t, url, map[string]any{"yaml": "mode: nonsense\n"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid yaml status = %d", resp.StatusCode)
	}
	if cur, _ := org.GetConfig("box-1"); cur.YAML != yamlCfg {
		t.Fatalf("yaml changed after rejected put: %q", cur.YAML)
	}

	// locked fields: the agent reported its mode/destinations; a pushed yaml
	// that rewrites them is refused
	reported := "mode: agent\nstream:\n  destinations:\n    - ws://hub:19999\n"
	if err := org.SetReport("box-1", reported, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	locked := "mode: agent\nstream:\n  destinations:\n    - ws://other:19999\n"
	resp, _, body := putHubConfig(t, url, map[string]any{"yaml": locked})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "locks") {
		t.Fatalf("locked yaml = %d %s", resp.StatusCode, body)
	}
	if cur, _ := org.GetConfig("box-1"); cur.YAML != yamlCfg {
		t.Fatalf("yaml changed after locked put: %q", cur.YAML)
	}

	// editing anything else on top of the reported file is fine
	allowed := "mode: agent\nstream:\n  destinations:\n    - ws://hub:19999\nglobal:\n  update_every: 5\n"
	if resp, _, _ = putHubConfig(t, url, map[string]any{"yaml": allowed}); resp.StatusCode != http.StatusOK {
		t.Fatalf("allowed yaml status = %d", resp.StatusCode)
	}

	// if_updated conflict
	cur, _ := org.GetConfig("box-1")
	resp, _, _ = putHubConfig(t, url, map[string]any{"yaml": allowed, "if_updated": cur.Updated + 9999})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("conflict status = %d", resp.StatusCode)
	}
	if after, _ := org.GetConfig("box-1"); after.Updated != cur.Updated {
		t.Fatal("revision advanced on conflict")
	}

	// the agent acking the current revision clears pending
	if err := org.SetApply("box-1", hub.ApplyState{Rev: cur.Updated, State: "applied", At: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	resp, got = getHubConfig(t, url, "")
	if resp.StatusCode != http.StatusOK || got.Pending {
		t.Fatalf("after apply = %d %+v", resp.StatusCode, got)
	}
	if got.Apply == nil || got.Apply.State != "applied" || got.Reported != reported {
		t.Fatalf("after apply = %+v", got)
	}
}

// Legacy callers (HubPanel) send only a disabled list with no yaml; that must
// keep working and never clobber a stored yaml.
func TestHubConfigDisabledOnlyCompat(t *testing.T) {
	hs, _, org, _ := newHubCfg(t, Options{})
	url := hs.URL + "/api/v1/hub/config?node=box-1"

	if _, err := org.SetConfig(hub.NodeConfig{NodeID: "box-1", YAML: "mode: agent\n"}); err != nil {
		t.Fatal(err)
	}
	resp, out, _ := putHubConfig(t, url, map[string]any{"disabled": []string{"nvidia"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put status = %d", resp.StatusCode)
	}
	if len(out.Disabled) != 1 || out.Disabled[0] != "nvidia" || out.YAML != "mode: agent\n" {
		t.Fatalf("put = %+v", out)
	}

	// a missing node parameter is a client error
	resp, _, _ = putHubConfig(t, hs.URL+"/api/v1/hub/config", map[string]any{"disabled": []string{"nvidia"}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no node status = %d", resp.StatusCode)
	}
}

func TestHubConfigPushOnline(t *testing.T) {
	hs, nodes, _, _ := newHubCfg(t, Options{})
	_, _, client := newAgent(t, hs.URL, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go client.Run(ctx)
	waitNode(t, nodes, "agent-1")

	// the test agent has no OnConfigFile hook, so the frame is queued and
	// dropped client-side; what matters here is that the push happened
	resp, out, _ := putHubConfig(t, hs.URL+"/api/v1/hub/config?node=agent-1", map[string]any{"yaml": "mode: agent\n"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put status = %d", resp.StatusCode)
	}
	if !out.Online || !out.Pushed {
		t.Fatalf("push = %+v", out)
	}
}

// The desired and reported config files carry credentials: a viewer sees the
// config envelope but neither text, and cannot write.
func TestHubConfigViewerHidesSecrets(t *testing.T) {
	hs, _, org, _ := newHubCfg(t, Options{Users: []User{
		{Name: "a", Token: "tok-admin", Role: RoleAdmin},
		{Name: "v", Token: "tok-view", Role: RoleViewer},
	}})
	url := hs.URL + "/api/v1/hub/config?node=box-1"
	yamlCfg := "mode: agent\nweb:\n  token: desired-secret\n"

	b, _ := json.Marshal(map[string]any{"yaml": yamlCfg})
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer tok-admin")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin put = %d", resp.StatusCode)
	}
	if err := org.SetReport("box-1", "mode: agent\nweb:\n  token: live-secret\n", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}

	resp, got := getHubConfig(t, url, "tok-view")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer get = %d", resp.StatusCode)
	}
	if got.YAML != "" || got.Reported != "" {
		t.Fatalf("viewer leaked secrets: %+v", got)
	}
	if got.NodeID != "box-1" || got.Updated == 0 {
		t.Fatalf("viewer get = %+v", got)
	}

	resp, got = getHubConfig(t, url, "tok-admin")
	if resp.StatusCode != http.StatusOK || got.YAML != yamlCfg || got.Reported == "" {
		t.Fatalf("admin get = %d %+v", resp.StatusCode, got)
	}

	b, _ = json.Marshal(map[string]any{"disabled": []string{"nvidia"}})
	req, _ = http.NewRequest(http.MethodPut, url, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer tok-view")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer put = %d", resp.StatusCode)
	}
}
