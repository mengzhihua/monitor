package collect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func newRedfishFleet(t *testing.T, targets ...redfishTarget) (*redfishCollector, *registry.Registry) {
	t.Helper()
	r := &redfishCollector{cfg: redfishConfig{Targets: targets, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "test", UpdateEvery: 1}, nil)
	if err := r.Init(reg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Stop)
	return r, reg
}
func redfishCharts(reg *registry.Registry, context string) []*registry.Chart {
	var out []*registry.Chart
	for _, ch := range reg.Charts() {
		if ch.Context == context {
			out = append(out, ch)
		}
	}
	return out
}

func TestRedfishFleetFollowsMembersAndPagination(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		user, pass, ok := r.BasicAuth()
		if !ok || user != "monitor" || pass != "test-only-password" {
			http.Error(w, "unauthorized", 401)
			return
		}
		switch r.URL.RequestURI() {
		case "/redfish/v1/Systems":
			io.WriteString(w, `{"Name":"Computer System Collection","Members":[{"@odata.id":"/redfish/v1/Systems/1"}],"Members@odata.nextLink":"?page=2"}`)
		case "/redfish/v1/Systems?page=2":
			io.WriteString(w, `{"Members":[{"@odata.id":"/redfish/v1/Systems/2"}]}`)
		case "/redfish/v1/Systems/1":
			io.WriteString(w, `{"Id":"1","Status":{"Health":"OK","HealthRollup":"Critical"},"PowerState":"On","ProcessorSummary":{"Count":2},"MemorySummary":{"TotalSystemMemoryGiB":64},"Manufacturer":"Vendor","Model":"Server"}`)
		case "/redfish/v1/Systems/2":
			io.WriteString(w, `{"Id":"2","PowerState":"Off"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c, reg := newRedfishFleet(t, redfishTarget{Name: "rack-a", URL: server.URL + "/redfish/v1", User: "monitor", Password: "test-only-password"})
	if requests.Load() != 0 {
		t.Fatal("fleet Init performed network requests")
	}
	if err := c.Collect(context.Background(), reg, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 4 {
		t.Fatalf("requests=%d", requests.Load())
	}
	health := redfishCharts(reg, "redfish.system_health_state")
	if len(health) != 2 {
		t.Fatalf("health charts=%d", len(health))
	}
	for _, ch := range health {
		_, v := ch.LastValues()
		if ch.LabelsSnapshot()["system"] == "1" && (v["critical"] != 1 || v["ok"] != 0) {
			t.Fatalf("rollup not respected: %v", v)
		}
		if ch.LabelsSnapshot()["system"] == "2" && (v["unknown"] != 1 || v["ok"] != 0) {
			t.Fatalf("missing health not unknown: %v", v)
		}
	}
	if got := redfishCharts(reg, "redfish.system_processors"); len(got) != 1 {
		t.Fatalf("processor charts=%d", len(got))
	}
	if got := redfishCharts(reg, "redfish.system_memory"); len(got) != 1 {
		t.Fatalf("memory charts=%d", len(got))
	}
	if got := redfishCharts(reg, "redfish.system_power_state"); len(got) != 2 {
		t.Fatalf("power charts=%d", len(got))
	}
	_, v := mustChart(t, reg, "redfish.target_status.rack-a").LastValues()
	if v["up"] != 1 {
		t.Fatalf("availability=%v", v)
	}
}

func TestRedfishPartialFailurePreservesAvailableSystemsAndRecovers(t *testing.T) {
	var broken atomic.Bool
	broken.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Systems":
			io.WriteString(w, `{"Members":[{"@odata.id":"/Systems/1"},{"@odata.id":"/Systems/2"}]}`)
		case "/Systems/1":
			io.WriteString(w, `{"Id":"1","Status":{"Health":"Warning"}}`)
		default:
			if broken.Load() {
				http.Error(w, "offline", 503)
				return
			}
			io.WriteString(w, `{"Id":"2","Status":{"Health":"OK"}}`)
		}
	}))
	defer server.Close()
	c, reg := newRedfishFleet(t, redfishTarget{Name: "bmc", URL: server.URL})
	if err := c.Collect(context.Background(), reg, time.Unix(100, 0)); err == nil {
		t.Fatal("partial failure was hidden")
	}
	if len(redfishCharts(reg, "redfish.system_health_state")) != 1 {
		t.Fatal("successful system was lost")
	}
	_, v := mustChart(t, reg, "redfish.target_status.bmc").LastValues()
	if v["down"] != 1 {
		t.Fatal(v)
	}
	broken.Store(false)
	if err := c.Collect(context.Background(), reg, time.Unix(101, 0)); err != nil {
		t.Fatal(err)
	}
	if len(redfishCharts(reg, "redfish.system_health_state")) != 2 {
		t.Fatal("system did not recover")
	}
	_, v = mustChart(t, reg, "redfish.target_status.bmc").LastValues()
	if v["up"] != 1 {
		t.Fatal(v)
	}
}

func TestRedfishRejectsCrossOriginLinksAndRedirects(t *testing.T) {
	for _, kind := range []string{"member", "pagination", "redirect"} {
		t.Run(kind, func(t *testing.T) {
			var foreignRequests atomic.Int32
			foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				foreignRequests.Add(1)
				io.WriteString(w, `{"Id":"stolen","Status":{"Health":"OK"}}`)
			}))
			defer foreign.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch kind {
				case "member":
					fmt.Fprintf(w, `{"Members":[{"@odata.id":%q}]}`, foreign.URL+"/Systems/1")
				case "pagination":
					fmt.Fprintf(w, `{"Members":[],"Members@odata.nextLink":%q}`, foreign.URL+"/Systems")
				case "redirect":
					http.Redirect(w, r, foreign.URL+"/Systems", http.StatusFound)
				}
			}))
			defer server.Close()
			c, reg := newRedfishFleet(t, redfishTarget{Name: "safe", URL: server.URL, User: "monitor", Password: "secret-marker"})
			err := c.Collect(context.Background(), reg, time.Unix(100, 0))
			if err == nil {
				t.Fatal("unsafe request accepted")
			}
			if strings.Contains(err.Error(), "secret-marker") {
				t.Fatal("credentials in error")
			}
			if foreignRequests.Load() != 0 {
				t.Fatalf("foreign endpoint contacted %d times", foreignRequests.Load())
			}
			if len(redfishCharts(reg, "redfish.system_health_state")) != 0 {
				t.Fatal("fabricated healthy system")
			}
		})
	}
}

func TestRedfishTLSVerificationAndUnknownHealth(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"Members":[{"Id":"1","Status":{}}]}`)
	}))
	defer server.Close()
	c, reg := newRedfishFleet(t, redfishTarget{Name: "verified", URL: server.URL}, redfishTarget{Name: "explicit", URL: server.URL, TLS: CollectorTLS{InsecureSkipVerify: true}})
	if err := c.Collect(context.Background(), reg, time.Unix(100, 0)); err == nil {
		t.Fatal("untrusted TLS accepted")
	}
	_, v := mustChart(t, reg, "redfish.target_status.verified").LastValues()
	if v["down"] != 1 {
		t.Fatal(v)
	}
	_, v = mustChart(t, reg, "redfish.target_status.explicit").LastValues()
	if v["up"] != 1 {
		t.Fatal(v)
	}
	health := redfishCharts(reg, "redfish.system_health_state")
	if len(health) != 1 {
		t.Fatal("incorrect health chart count")
	}
	_, v = health[0].LastValues()
	if v["unknown"] != 1 || v["ok"] != 0 {
		t.Fatal(v)
	}
}

func TestRedfishMalformedAndUnauthorizedResponses(t *testing.T) {
	for _, body := range []string{"null", "[]", "invalid", `{"Name":"Collection"}`, `{"Members":{}}`, `{"Members":[]}`, `{"Members":[null]}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
			defer server.Close()
			c, reg := newRedfishFleet(t, redfishTarget{Name: "test", URL: server.URL})
			if err := c.Collect(context.Background(), reg, time.Unix(100, 0)); err == nil {
				t.Fatal("invalid response accepted")
			}
			if len(redfishCharts(reg, "redfish.system_health_state")) != 0 {
				t.Fatal("fabricated system")
			}
		})
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "denied", 401) }))
	defer server.Close()
	c, reg := newRedfishFleet(t, redfishTarget{Name: "auth", URL: server.URL})
	if err := c.Collect(context.Background(), reg, time.Unix(100, 0)); err == nil {
		t.Fatal("unauthorized response accepted")
	}
	if calls.Load() != 1 {
		t.Fatal("401 triggered fallback request")
	}
}

func TestRedfishFleetCancellationDoesNotPublishFailures(t *testing.T) {
	entered := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	c, reg := newRedfishFleet(t, redfishTarget{Name: "cancelled", URL: server.URL, Timeout: time.Minute})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Collect(ctx, reg, time.Unix(100, 0)) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request not started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation stalled")
	}
	ts, _ := mustChart(t, reg, "redfish.target_status.cancelled").LastValues()
	if ts != 0 {
		t.Fatal("cancellation published failure")
	}
}

func TestRedfishFleetConcurrencyAndStableIdentities(t *testing.T) {
	var active, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for p := peak.Load(); n > p; p = peak.Load() {
			if peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		io.WriteString(w, `{"Members":[{"@odata.id":"/Systems/1","Id":"same","Status":{"Health":"OK"}},{"@odata.id":"/Systems/2","Id":"same","Status":{"Health":"Warning"}}]}`)
	}))
	defer server.Close()
	var targets []redfishTarget
	for i := 0; i < 9; i++ {
		targets = append(targets, redfishTarget{Name: fmt.Sprintf("bmc%d", i), URL: server.URL})
	}
	c, reg := newRedfishFleet(t, targets...)
	if err := c.Collect(context.Background(), reg, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if peak.Load() > 4 || peak.Load() < 2 {
		t.Fatalf("peak concurrency=%d", peak.Load())
	}
	if len(redfishCharts(reg, "redfish.system_health_state")) != 18 {
		t.Fatal("colliding target/system identities")
	}
	before := map[string]bool{}
	for _, ch := range redfishCharts(reg, "redfish.system_health_state") {
		before[ch.ID] = true
	}
	if err := c.Collect(context.Background(), reg, time.Unix(101, 0)); err != nil {
		t.Fatal(err)
	}
	for _, ch := range redfishCharts(reg, "redfish.system_health_state") {
		if !before[ch.ID] {
			t.Fatal("unstable identity")
		}
	}
}

func TestRedfishBoundedPaginationAndTargetValidation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		fmt.Fprintf(w, `{"Members":[],"Members@odata.nextLink":"?page=%d"}`, n)
	}))
	defer server.Close()
	c, reg := newRedfishFleet(t, redfishTarget{Name: "bound", URL: server.URL})
	if err := c.Collect(context.Background(), reg, time.Unix(100, 0)); err == nil {
		t.Fatal("unbounded pagination accepted")
	}
	if calls.Load() != redfishMaxPages {
		t.Fatalf("pages=%d", calls.Load())
	}
	for _, targets := range [][]redfishTarget{
		{{Name: "same", URL: server.URL}, {Name: "same", URL: server.URL}},
		{{Name: "bad.name", URL: server.URL}},
		{{Name: "safe", URL: "https://user:secret-marker@bmc/redfish/v1"}},
		{{Name: strings.Repeat("n", 49), URL: server.URL}},
		make([]redfishTarget, 65),
	} {
		bad := &redfishCollector{cfg: redfishConfig{Targets: targets}}
		if err := bad.Configure(func(any) error { return nil }); err == nil || strings.Contains(err.Error(), "secret-marker") {
			t.Fatalf("invalid/secret-bearing config validation: %v", err)
		}
	}
}

func TestRedfishTargetTimeoutBoundsWholeWalk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Systems" {
			io.WriteString(w, `{"Members":[{"@odata.id":"/Systems/1"},{"@odata.id":"/Systems/2"}]}`)
			return
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	c, reg := newRedfishFleet(t, redfishTarget{Name: "timeout", URL: server.URL, Timeout: 50 * time.Millisecond})
	start := time.Now()
	if err := c.Collect(context.Background(), reg, time.Unix(100, 0)); err == nil {
		t.Fatal("timeout hidden")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("target timeout did not bound walk")
	}
	_, v := mustChart(t, reg, "redfish.target_status.timeout").LastValues()
	if v["down"] != 1 {
		t.Fatal(v)
	}
}

func TestRedfishRejectsBrokenResourcesAndBoundsSystems(t *testing.T) {
	for _, kind := range []string{"empty-resource", "empty-fallback", "pagination-cycle", "system-limit", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch kind {
				case "empty-resource":
					if r.URL.Path == "/Systems" {
						io.WriteString(w, `{"Members":[{"@odata.id":"/Systems/1"}]}`)
					} else {
						io.WriteString(w, `{}`)
					}
				case "empty-fallback":
					if r.URL.Path == "/Systems" {
						http.NotFound(w, r)
					} else {
						io.WriteString(w, `{}`)
					}
				case "pagination-cycle":
					io.WriteString(w, `{"Members":[],"Members@odata.nextLink":"/Systems"}`)
				case "system-limit":
					io.WriteString(w, `{"Members":[`)
					for i := 0; i < redfishMaxSystems+1; i++ {
						if i > 0 {
							io.WriteString(w, ",")
						}
						fmt.Fprintf(w, `{"Id":"%d","Status":{"Health":"OK"}}`, i)
					}
					io.WriteString(w, `]}`)
				case "oversized":
					io.WriteString(w, strings.Repeat(" ", redfishMaxBody+1))
				}
			}))
			defer server.Close()
			c, reg := newRedfishFleet(t, redfishTarget{Name: "test", URL: server.URL})
			if err := c.Collect(context.Background(), reg, time.Unix(100, 0)); err == nil {
				t.Fatal("broken response accepted")
			}
			if kind == "system-limit" {
				if n := len(redfishCharts(reg, "redfish.system_health_state")); n != redfishMaxSystems {
					t.Fatalf("systems=%d", n)
				}
			} else if len(redfishCharts(reg, "redfish.system_health_state")) != 0 {
				t.Fatal("fabricated system")
			}
			if kind == "pagination-cycle" && calls.Load() != 1 {
				t.Fatalf("cycle made %d requests", calls.Load())
			}
		})
	}
}

func TestRedfishExplicitEmptyFleetDoesNotProbeLegacyTarget(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"Members":[{"Id":"legacy","Status":{"Health":"OK"}}]}`)
	}))
	defer server.Close()
	reg := registry.New(&registry.Host{Hostname: "test", UpdateEvery: 1}, nil)
	empty := &redfishCollector{cfg: redfishConfig{Targets: []redfishTarget{}}}
	if err := empty.Init(reg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(empty.Stop)
	if err := empty.Collect(context.Background(), reg, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || len(reg.Charts()) != 0 || len(empty.targets) != 0 {
		t.Fatal("empty fleet probed legacy target")
	}
	legacy := &redfishCollector{cfg: redfishConfig{URL: server.URL}}
	if err := legacy.Init(reg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(legacy.Stop)
	if err := legacy.Collect(context.Background(), reg, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("absent targets did not preserve legacy mode")
	}
	if _, ok := reg.Chart("redfish.system_health_state.legacy"); !ok {
		t.Fatal("legacy chart identity changed")
	}
	for _, timeout := range []time.Duration{-time.Second, time.Minute + 1} {
		bad := &redfishCollector{cfg: redfishConfig{Targets: []redfishTarget{{Name: "bad", URL: server.URL, Timeout: timeout}}}}
		if err := bad.Configure(func(any) error { return nil }); err == nil {
			t.Fatal("invalid timeout accepted")
		}
	}
}

func TestRedfishInventoryDoesNotInventMissingOrInvalidValues(t *testing.T) {
	obj := map[string]any{"Id": "1", "ProcessorSummary": map[string]any{"Count": float64(2.5)}, "MemorySummary": map[string]any{"TotalSystemMemoryGiB": "64"}}
	s := parseRedfishSystem(obj, "/Systems/1")
	if s.processors != nil || s.memory != nil {
		t.Fatal("invalid inventory treated as measured values")
	}
	obj["ProcessorSummary"] = map[string]any{"Count": float64(0)}
	obj["MemorySummary"] = map[string]any{"TotalSystemMemoryGiB": float64(0)}
	s = parseRedfishSystem(obj, "/Systems/1")
	if s.processors == nil || s.memory == nil || *s.processors != 0 || *s.memory != 0 {
		t.Fatal("explicit zero inventory lost")
	}
	if validRedfishIdentity(map[string]any{"Id": strings.Repeat("x", 257)}) {
		t.Fatal("unbounded identity accepted")
	}
	if len(redfishText(strings.Repeat("x", 1024))) != 256 {
		t.Fatal("unbounded retained metadata")
	}
}

func TestRedfishFleetKeepsCompletedTargetsWhenParentDeadlineExpires(t *testing.T) {
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"Members":[{"Id":"1","Status":{"Health":"OK"}}]}`)
	}))
	defer fast.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer slow.Close()
	c, reg := newRedfishFleet(t, redfishTarget{Name: "fast", URL: fast.URL}, redfishTarget{Name: "slow", URL: slow.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := c.Collect(ctx, reg, time.Unix(100, 0))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation error lost: %v", err)
	}
	_, values := mustChart(t, reg, "redfish.target_status.fast").LastValues()
	if values["up"] != 1 {
		t.Fatal("completed target lost", values)
	}
	ts, _ := mustChart(t, reg, "redfish.target_status.slow").LastValues()
	if ts != 0 {
		t.Fatal("interrupted target published false failure")
	}
	if len(redfishCharts(reg, "redfish.system_health_state")) != 1 {
		t.Fatal("completed system lost")
	}
}

func TestRedfishRejectsMixedLegacyAndFleetTargets(t *testing.T) {
	for _, targets := range [][]redfishTarget{{}, {{Name: "bmc", URL: "https://bmc/redfish/v1"}}} {
		c := &redfishCollector{cfg: redfishConfig{URL: "https://legacy/redfish/v1", Targets: targets}}
		if err := c.Configure(func(any) error { return nil }); err == nil {
			t.Fatal("mixed targets accepted")
		}
	}
}
