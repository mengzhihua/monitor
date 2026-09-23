package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/hub"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/stream"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func newInfoHub(tb testing.TB, opt hub.Options) (*Server, *hub.Nodes) {
	tb.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := tsdb.Open(tsdb.Options{Dir: tb.TempDir(), Logger: log})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = db.Close() })
	opt.Logger = log
	nodes, err := hub.Open(db, tb.TempDir(), opt)
	if err != nil {
		tb.Fatal(err)
	}
	reg := registry.New(&registry.Host{ID: "info-hub", Hostname: "hub", UpdateEvery: 1}, db)
	reg.AddChart(&registry.Chart{ID: "local.chart", Dimensions: []*registry.Dimension{{ID: "a"}, {ID: "b"}}})
	sched := collect.NewScheduler(reg, log, collect.Options{Names: []string{"none"}})
	srv, err := New(reg, db, sched, Options{Mode: "hub", Nodes: nodes, StartedAt: time.Now(), Logger: log})
	if err != nil {
		tb.Fatal(err)
	}
	return srv, nodes
}

func assertHubInfo(t *testing.T, srv *Server, nodes, live int, enabled bool, storage string) {
	t.Helper()
	for _, version := range []int{1, 3} {
		request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v%d/info", version), nil)
		response := httptest.NewRecorder()
		srv.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("v%d info: status %d: %s", version, response.Code, response.Body.String())
		}
		var got struct {
			API              *int `json:"api"`
			Mode             string
			Host             registry.Host
			NodesCount       int  `json:"nodes_count"`
			ChartsCount      int  `json:"charts_count"`
			MetricsCount     int  `json:"metrics_count"`
			StreamingEnabled bool `json:"streaming_enabled"`
			ACLK             struct {
				Available, Online, Claimed bool
				Nodes, Live                int
				Protocol, Storage          string
			}
		}
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if version == 1 && got.API != nil || version == 3 && (got.API == nil || *got.API != 3) {
			t.Fatalf("v%d info: api = %v", version, got.API)
		}
		if got.Mode != "hub" || got.Host.ID != "info-hub" || got.ChartsCount != 1 || got.MetricsCount != 2 {
			t.Fatalf("v%d info changed local host metadata: %+v", version, got)
		}
		if got.NodesCount != nodes+1 || got.StreamingEnabled != enabled || got.ACLK.Nodes != nodes || got.ACLK.Live != live ||
			!got.ACLK.Available || got.ACLK.Online != (enabled && live > 0) || got.ACLK.Claimed ||
			got.ACLK.Protocol != "stream+mqtt" || got.ACLK.Storage != storage {
			t.Fatalf("v%d info: got %+v; want remote nodes=%d live=%d enabled=%t storage=%s", version, got, nodes, live, enabled, storage)
		}
	}
}

func TestInfoHubReplicaChanges(t *testing.T) {
	for _, storage := range []string{"full", "proxy"} {
		t.Run(storage, func(t *testing.T) {
			var enabled atomic.Bool
			enabled.Store(true)
			srv, nodes := newInfoHub(t, hub.Options{Storage: storage, ExtraKeys: func() []string {
				if enabled.Load() {
					return []string{testKey}
				}
				return nil
			}})
			assertHubInfo(t, srv, 0, 0, true, storage)
			chart := &registry.Chart{ID: "remote.chart", Dimensions: []*registry.Dimension{{ID: "value"}}}
			accept := func(id string, sampleTime time.Time) *hub.Node {
				t.Helper()
				var samples []hub.ReplicaSample
				if !sampleTime.IsZero() {
					samples = []hub.ReplicaSample{{Chart: chart.ID, T: sampleTime.Unix(), V: map[string]float64{"value": 1}}}
				}
				n, err := nodes.AcceptReplica(registry.Host{ID: id, Hostname: id, UpdateEvery: 1}, []*registry.Chart{chart}, samples, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				return n
			}
			for _, state := range []struct {
				status string
				at     time.Time
			}{{hub.StatusLive, time.Now()}, {hub.StatusStale, time.Now().Add(-time.Minute)}, {hub.StatusOffline, time.Time{}}} {
				n := accept(state.status, state.at)
				if got := n.Status(time.Now()); got != state.status {
					t.Fatalf("replica %s: status = %s", state.status, got)
				}
			}
			assertHubInfo(t, srv, 3, 1, true, storage)
			accept(hub.StatusStale, time.Now())
			assertHubInfo(t, srv, 3, 2, true, storage)
			enabled.Store(false)
			assertHubInfo(t, srv, 3, 2, false, storage)
			for _, id := range []string{hub.StatusLive, hub.StatusStale} {
				if err := nodes.Forget(id); err != nil {
					t.Fatal(err)
				}
			}
			enabled.Store(true)
			assertHubInfo(t, srv, 1, 0, true, storage)
		})
	}
}

func TestInfoHubUsesOneNodeSnapshot(t *testing.T) {
	var nodes *hub.Nodes
	var added atomic.Bool
	srv, nodes := newInfoHub(t, hub.Options{ExtraKeys: func() []string {
		// Inject a membership change after the first node count is read, as an
		// agent joining concurrently could. Counts in this response must agree;
		// the next request must see the new node.
		if added.CompareAndSwap(false, true) {
			if _, err := nodes.AcceptReplica(registry.Host{ID: "joining", Hostname: "joining"}, nil, nil, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		return []string{testKey}
	}})
	response := httptest.NewRecorder()
	srv.handleInfo(response, httptest.NewRequest(http.MethodGet, "/api/v1/info", nil))
	var got struct {
		NodesCount int `json:"nodes_count"`
		ACLK       struct{ Nodes, Live int }
	}
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.NodesCount != 1 || got.ACLK.Nodes != 0 || got.ACLK.Live != 0 {
		t.Fatalf("inconsistent node snapshot: %+v", got)
	}
	assertHubInfo(t, srv, 1, 0, true, "full")
}

func TestInfoHubStreamStatusChanges(t *testing.T) {
	srv, nodes := newInfoHub(t, hub.Options{Keys: []string{testKey}})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	connect := func() *websocket.Conn {
		t.Helper()
		ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+stream.Path,
			http.Header{"Authorization": {"Bearer " + testKey}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ws.Close() })
		_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		if err := ws.WriteJSON(stream.Frame{Type: stream.TypeHello, Host: &registry.Host{ID: "agent", Hostname: "agent", UpdateEvery: 1}}); err != nil {
			t.Fatal(err)
		}
		var reply stream.Frame
		if err := ws.ReadJSON(&reply); err != nil || reply.Type != stream.TypeWelcome {
			t.Fatalf("hello: reply=%+v err=%v", reply, err)
		}
		return ws
	}
	ws := connect()
	assertHubInfo(t, srv, 1, 0, true, "full") // Connected without a live sample is stale.
	node, _ := nodes.Get("agent")
	chart := &registry.Chart{ID: "remote.chart", Dimensions: []*registry.Dimension{{ID: "value"}}}
	if err := ws.WriteJSON(stream.Frame{Type: stream.TypeChart, Chart: stream.DefOf(chart)}); err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		ago    time.Duration
		replay bool
		live   int
	}{{time.Minute, false, 0}, {30 * time.Second, true, 0}, {0, false, 1}} {
		at := time.Now().Add(-sample.ago).Unix()
		if err := ws.WriteJSON(stream.Frame{Type: stream.TypeData, ChartID: chart.ID, T: at, V: map[string]float64{"value": 1}, Replay: sample.replay}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "stream sample", func() bool {
			_, last, ok := node.DB().Bounds(registry.SeriesID(chart.ID, "value"))
			return ok && last == at && (node.Status(time.Now()) == hub.StatusLive) == (sample.live == 1)
		})
		assertHubInfo(t, srv, 1, sample.live, true, "full")
	}
	_ = ws.Close()
	waitFor(t, "agent offline", func() bool { return node.Status(time.Now()) == hub.StatusOffline })
	assertHubInfo(t, srv, 1, 0, true, "full")
	ws = connect()
	assertHubInfo(t, srv, 1, 0, true, "full") // Reconnect must not reuse previous liveness.
	_ = ws.Close()
	waitFor(t, "reconnected agent offline", func() bool { return node.Status(time.Now()) == hub.StatusOffline })
}

// Remote charts should not increase /info work: only node status contributes to
// the aggregate counts, while detailed chart metadata belongs to other routes.
func BenchmarkHubInfo(b *testing.B) {
	for _, remoteNodes := range []int{1, 20} {
		for _, chartsPerNode := range []int{0, 250} {
			b.Run(fmt.Sprintf("nodes=%d/charts_per_node=%d", remoteNodes, chartsPerNode), func(b *testing.B) {
				srv, nodes := newInfoHub(b, hub.Options{Keys: []string{testKey}})
				charts := make([]*registry.Chart, chartsPerNode)
				for i := range charts {
					charts[i] = &registry.Chart{ID: fmt.Sprintf("remote.chart%d", i), Dimensions: []*registry.Dimension{{ID: "value"}}}
				}
				for i := 0; i < remoteNodes; i++ {
					id := fmt.Sprintf("agent-%d", i)
					if _, err := nodes.AcceptReplica(registry.Host{ID: id, Hostname: id, UpdateEvery: 1}, charts, nil, time.Now()); err != nil {
						b.Fatal(err)
					}
				}
				request := httptest.NewRequest(http.MethodGet, "/api/v1/info", nil)
				writer := discardResponse{header: make(http.Header)}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					srv.handleInfo(writer, request)
				}
			})
		}
	}
}
