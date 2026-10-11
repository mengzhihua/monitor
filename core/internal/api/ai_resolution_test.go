package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/aianalysis"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/hub"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

func apiFixturePlan() aianalysis.Plan {
	return aianalysis.Plan{Summary: "Review the observed metrics", Hypotheses: []aianalysis.Hypothesis{{Explanation: "Unknown workload", Evidence: "Short window", Confidence: "low"}}, Steps: []aianalysis.PlanStep{
		{ID: "s1", Title: "Inspect metrics", Kind: "diagnostic", Instruction: "Inspect available evidence", Expected: "Identify scope", Risk: "low", Chart: "test.analysis", Dimension: "value"},
		{ID: "s2", Title: "Review results", Kind: "verification", Instruction: "Read new monitoring samples", Expected: "New observations", Risk: "low"},
	}}
}
func readResolution(t *testing.T, w *httptest.ResponseRecorder) aianalysis.Resolution {
	t.Helper()
	var r aianalysis.Resolution
	if json.Unmarshal(w.Body.Bytes(), &r) != nil || r.ID == "" {
		t.Fatal("invalid resolution response")
	}
	return r
}
func TestAIResolutionLifecycleFrozenEvidenceAndNoAutomaticCalls(t *testing.T) {
	var calls atomic.Int32
	submittedChannel := make(chan string, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		submittedChannel <- body.Messages[1].Content
		p, _ := json.Marshal(apiFixturePlan())
		writeJSON(w, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(p)}}}})
	}))
	defer provider.Close()
	opt := Options{Token: "ai-admin-token", OperationsDir: t.TempDir(), Users: []User{{Name: "same-name", Token: "ai-viewer-token", Role: RoleViewer}}}
	s, _ := aiTestServer(t, opt)
	if err := s.reg.Collect("test.analysis", time.Now().Add(-time.Second), map[string]float64{"value": 20}); err != nil {
		t.Fatal(err)
	}
	viewRequest(t, s, "PUT", aiSettingsPath, "ai-admin-token", aiJSON(t, map[string]any{"base_url": provider.URL + "/v1", "allow_private": true, "model": "fixture", "revision": 0}), 200)
	p := aiReadPreview(t, s, "ai-admin-token", aiPreviewBody)
	w := viewRequest(t, s, "POST", aiAnalyzePath, "ai-admin-token", aiJSON(t, map[string]any{"preview_id": p.ID, "revision": 1, "mode": "resolution", "question": "Help investigate"}), 200)
	var generated struct {
		Resolution aianalysis.Resolution `json:"resolution"`
	}
	if json.Unmarshal(w.Body.Bytes(), &generated) != nil {
		t.Fatal("invalid generation")
	}
	r := generated.Resolution
	submitted := <-submittedChannel
	if r.ID == "" || calls.Load() != 1 || !strings.Contains(submitted, "JSON") {
		t.Fatal("structured generation missing")
	}
	var frozen aiSnapshot
	_ = json.Unmarshal(r.Baseline, &frozen)
	if frozen.Before != p.Snapshot.Before || frozen.Charts[0].Dimensions[0].Average == nil {
		t.Fatal("baseline changed")
	}
	viewRequest(t, s, "GET", "/api/v1/ai/resolutions?id="+r.ID, "ai-viewer-token", "", 404)
	w = viewRequest(t, s, "POST", "/api/v1/ai/resolutions", "ai-admin-token", aiJSON(t, map[string]any{"id": r.ID, "revision": 1, "step_id": "s1", "status": "done", "note": "manual-operator-note-not-for-model"}), 200)
	r = readResolution(t, w)
	viewRequest(t, s, "POST", "/api/v1/ai/resolutions", "ai-admin-token", aiJSON(t, map[string]any{"id": r.ID, "revision": 1, "step_id": "s1", "status": "skipped"}), 409)
	w = viewRequest(t, s, "POST", "/api/v1/ai/resolutions/review", "ai-admin-token", aiJSON(t, map[string]any{"id": r.ID, "revision": 2}), 200)
	r = readResolution(t, w)
	var review aiReview
	_ = json.Unmarshal(r.Review, &review)
	if review.Outcome != "awaiting_new_data" {
		t.Fatalf("old samples counted as repaired: %s", review.Outcome)
	}
	if calls.Load() != 1 || strings.Contains(submitted, "manual-operator-note") {
		t.Fatal("manual progress or review contacted model")
	}
	s, _ = aiTestServer(t, opt)
	r = readResolution(t, viewRequest(t, s, "GET", "/api/v1/ai/resolutions?id="+r.ID, "ai-admin-token", "", 200))
	if r.Revision != 3 || r.Progress[0].Status != "done" {
		t.Fatal("restart lost workflow")
	}
	viewRequest(t, s, "DELETE", "/api/v1/ai/resolutions", "ai-admin-token", aiJSON(t, map[string]any{"id": r.ID, "revision": 3}), 200)
	viewRequest(t, s, "GET", "/api/v1/ai/resolutions?id="+r.ID, "ai-admin-token", "", 404)
}
func TestAIResolutionValidationDoesNotSaveProviderProse(t *testing.T) {
	for _, text := range []string{"run a command now", `{"summary":"invalid","steps":[]}`, func() string {
		p := apiFixturePlan()
		p.Steps[0].Chart = "invented.chart"
		raw, _ := json.Marshal(p)
		return string(raw)
	}()} {
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": text}}}})
		}))
		s, _ := aiTestServer(t, Options{Token: "ai-admin-token"})
		viewRequest(t, s, "PUT", aiSettingsPath, "ai-admin-token", aiJSON(t, map[string]any{"base_url": provider.URL + "/v1", "allow_private": true, "model": "fixture", "revision": 0}), 200)
		p := aiReadPreview(t, s, "ai-admin-token", aiPreviewBody)
		viewRequest(t, s, "POST", aiAnalyzePath, "ai-admin-token", aiJSON(t, map[string]any{"preview_id": p.ID, "revision": 1, "mode": "resolution"}), 502)
		list, _ := s.analysis.resolutions.List(viewPrincipal("legacy", "ai-admin-token"))
		if len(list) != 0 {
			t.Fatal("invalid plan persisted")
		}
		provider.Close()
	}
}

func TestAIResolutionEscapedProviderKeyIsNotSaved(t *testing.T) {
	const key = "private-provider-secret"
	plan := apiFixturePlan()
	plan.Summary = key
	raw, _ := json.Marshal(plan)
	escaped := strings.ReplaceAll(string(raw), key, `\u0070rivate-provider-secret`)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": escaped}}}})
	}))
	defer provider.Close()
	s, _ := aiTestServer(t, Options{Token: "ai-admin-token"})
	viewRequest(t, s, "PUT", aiSettingsPath, "ai-admin-token", aiJSON(t, map[string]any{"base_url": provider.URL + "/v1", "model": "fixture", "api_key": key, "allow_private": true, "revision": 0}), 200)
	p := aiReadPreview(t, s, "ai-admin-token", aiPreviewBody)
	w := viewRequest(t, s, "POST", aiAnalyzePath, "ai-admin-token", aiJSON(t, map[string]any{"preview_id": p.ID, "revision": 1, "mode": "resolution"}), 200)
	if strings.Contains(w.Body.String(), key) || !strings.Contains(w.Body.String(), "[redacted]") {
		t.Fatal("decoded structured plan exposed provider key")
	}
}
func TestAIResolutionRoomScopesAndViewerPersonalUpdates(t *testing.T) {
	s, _ := aiTestServer(t, Options{Users: []User{{Name: "reader", Token: "ai-viewer-token", Role: RoleViewer, Rooms: []string{"allowed"}}}})
	nodes, err := hub.Open(s.db, t.TempDir(), hub.Options{})
	if err != nil {
		t.Fatal(err)
	}
	node, err := nodes.AcceptReplica(registry.Host{ID: "resolution-node", Hostname: "remote", UpdateEvery: 1}, []*registry.Chart{{ID: "test.analysis", Dimensions: []*registry.Dimension{{ID: "value"}}}}, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.opt.Nodes = nodes
	node.SetMembership("space", "allowed")
	p := aiReadPreview(t, s, "ai-viewer-token", `{"node":"resolution-node","charts":["test.analysis"],"window":300}`)
	baseline, _ := json.Marshal(p.Snapshot)
	r, err := s.analysis.resolutions.Create(viewPrincipal("static", "ai-viewer-token"), aianalysis.Resolution{Node: p.Snapshot.Node, Hostname: p.Snapshot.Hostname, Model: "fixture", Plan: apiFixturePlan(), Baseline: baseline})
	if err != nil {
		t.Fatal(err)
	}
	viewRequest(t, s, "POST", "/api/v1/ai/resolutions", "ai-viewer-token", aiJSON(t, map[string]any{"id": r.ID, "revision": 1, "step_id": "s1", "status": "done"}), 200)
	node.SetMembership("space", "forbidden")
	for _, path := range []string{"/api/v1/ai/resolutions?id=" + r.ID, "/api/v1/ai/resolutions?id=" + r.ID + "&node=local"} {
		viewRequest(t, s, "GET", path, "ai-viewer-token", "", 403)
	}
	for _, path := range []string{"/api/v1/ai/resolutions", "/api/v1/ai/resolutions/review"} {
		viewRequest(t, s, "POST", path+"?node=local", "ai-viewer-token", aiJSON(t, map[string]any{"id": r.ID, "revision": 2}), 403)
	}
	w := viewRequest(t, s, "GET", "/api/v1/ai/resolutions", "ai-viewer-token", "", 200)
	if strings.Contains(w.Body.String(), r.ID) {
		t.Fatal("list leaks forbidden-room plan")
	}
	share := s.shares.issue("", time.Hour).Token
	viewRequest(t, s, "GET", "/api/v1/ai/resolutions", share, "", 403)
}
func TestAIResolutionReviewStatesAndProblemEpisodeBinding(t *testing.T) {
	value := 12.0
	baseline := aiSnapshot{Before: 100, Problem: &aiAlarm{Name: "cpu", Chart: "test", ProblemID: "episode-one"}, Charts: []aiChart{{ID: "test", Units: "%", Dimensions: []aiDimension{{ID: "value", Average: &value}}}}}
	current := aiSnapshot{GeneratedAt: 200, Charts: []aiChart{{ID: "test", Units: "%", LastCollectedAt: 200, State: "fresh", Dimensions: []aiDimension{{ID: "value", Average: &value, Count: 1}}}}, Alarms: []aiAlarm{{Name: "cpu", Chart: "test", ProblemID: "episode-one", Status: "WARNING", State: "fresh", UpdatedAt: 200}}}
	for _, tc := range []struct{ status, id, state, want string }{{"WARNING", "episode-one", "fresh", "still_active"}, {"CRITICAL", "episode-two", "fresh", "episode_changed"}, {"CLEAR", "episode-two", "fresh", "alarm_recovered"}, {"CLEAR", "episode-two", "stale", "insufficient_data"}} {
		current.Alarms[0].Status, current.Alarms[0].ProblemID, current.Alarms[0].State = tc.status, tc.id, tc.state
		if got := compareAISnapshots(baseline, current); got.Outcome != tc.want {
			t.Fatalf("%+v -> %s", tc, got.Outcome)
		}
	}
	current.Charts[0].State = "stale"
	if compareAISnapshots(baseline, current).Outcome != "insufficient_data" {
		t.Fatal("stale metrics imply success")
	}
	s, _ := aiTestServer(t, Options{Token: "ai-admin-token"})
	rules, err := health.ParseRules([]byte("alarms:\n  - name: warning\n    on: test.analysis\n    calc: '$value'\n    every: 1s\n    warn: '$this > 0'\n"), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	engine, err := health.New(s.reg, s.db, health.Options{Rules: rules, SilenceAll: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Close)
	s.opt.Health = engine
	if err := s.reg.Collect("test.analysis", time.Now(), map[string]float64{"value": 20}); err != nil {
		t.Fatal(err)
	}
	engine.Tick(time.Now())
	a := s.opt.Health.Alarms()[0]
	id := problemID(s.reg.Host.ID, a)
	p := aiReadPreview(t, s, "ai-admin-token", fmt.Sprintf(`{"node":"","charts":["test.analysis"],"window":300,"problem_id":%q}`, id))
	if p.Snapshot.Problem == nil {
		t.Fatal("problem not pinned")
	}
	viewRequest(t, s, "POST", aiPreviewPath, "ai-admin-token", fmt.Sprintf(`{"node":"","charts":["test.analysis"],"window":300,"problem_id":%q}`, strings.Repeat("f", 64)), 409)
}
