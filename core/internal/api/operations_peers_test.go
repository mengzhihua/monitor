package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/hub"
)

func operationPeerFixture(t *testing.T, count int, alarm http.HandlerFunc) *httptest.Server {
	t.Helper()
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/nodes" {
			nodes := make([]hub.Info, count)
			for i := range nodes {
				nodes[i] = hub.Info{ID: fmt.Sprintf("peer-%02d", i), Hostname: fmt.Sprintf("box-%02d", i), Status: hub.StatusLive}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": nodes})
			return
		}
		alarm(w, r)
	}))
	t.Cleanup(peer.Close)
	cluster := hub.NewCluster([]string{peer.URL}, "", nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go cluster.Run(ctx)
	deadline := time.After(2 * time.Second)
	for !cluster.Has("peer-00") {
		select {
		case <-deadline:
			t.Fatal("peer catalog did not become available")
		case <-time.After(10 * time.Millisecond):
		}
	}
	ts, _ := newTestServer(t, Options{Cluster: cluster})
	return ts
}

func TestOperationsPeerReadsShareBoundedConcurrency(t *testing.T) {
	started := make(chan struct{}, 64)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var reads atomic.Int32
	ts := operationPeerFixture(t, 24, func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		now := time.Now().Unix()
		_ = json.NewEncoder(w).Encode(map[string]any{"alarms": map[string]health.Alarm{"warning": {
			Name: "warning", Chart: "system.ram", Status: health.StatusWarning, LastUpdated: now, LastStatusChange: now,
		}}})
	})
	// Two viewers must share the outbound limit rather than multiplying it.
	results := make(chan error, 2)
	for range 2 {
		go func() {
			resp, err := http.Get(ts.URL + "/api/v1/operations")
			if err != nil {
				results <- err
				return
			}
			defer resp.Body.Close()
			var snap operationsSnapshot
			err = json.NewDecoder(resp.Body).Decode(&snap)
			if err == nil && (snap.Summary["warning"] != 24 || len(snap.Problems) != 24 || len(snap.Nodes) != 25) {
				err = fmt.Errorf("incomplete snapshot: nodes=%d problems=%d summary=%v", len(snap.Nodes), len(snap.Problems), snap.Summary)
			}
			results <- err
		}()
	}
	for range operationPeerConcurrency {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			once.Do(func() { close(release) })
			t.Fatal("peer reads serialized instead of running concurrently")
		}
	}
	select {
	case <-started:
		once.Do(func() { close(release) })
		t.Fatal("concurrent viewers exceeded the shared peer limit")
	case <-time.After(50 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if reads.Load() != 48 {
		t.Fatalf("peer reads = %d, want 48", reads.Load())
	}
}

func TestOperationsPeerBudgetAndInvalidLimit(t *testing.T) {
	var reads atomic.Int32
	var active atomic.Int32
	ts := operationPeerFixture(t, 20, func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		active.Add(1)
		defer active.Add(-1)
		<-r.Context().Done()
	})
	resp, err := http.Get(ts.URL + "/api/v1/operations?limit=201")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || reads.Load() != 0 {
		t.Fatalf("invalid query must not contact peers: status=%d reads=%d", resp.StatusCode, reads.Load())
	}
	client := &http.Client{Timeout: operationPeerBudget + 3*time.Second}
	resp, err = client.Get(ts.URL + "/api/v1/operations")
	if err != nil {
		t.Fatalf("whole snapshot must finish within one peer budget: %v", err)
	}
	defer resp.Body.Close()
	var snap operationsSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Nodes) != 21 || len(snap.Problems) != 0 || snap.Summary["coverage_unknown"] != 21 {
		t.Fatalf("timed out coverage was not preserved: %+v", snap)
	}
	if n := reads.Load(); n < 1 || n > operationPeerConcurrency {
		t.Fatalf("unstarted peer requests should be dropped at the deadline: %d", n)
	}
	deadline := time.Now().Add(time.Second)
	for active.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if active.Load() != 0 {
		t.Fatal("peer reads survived the snapshot deadline")
	}
}

func TestOperationsPeerReadsStopOnCallerCancellation(t *testing.T) {
	started := make(chan struct{}, 32)
	var active atomic.Int32
	ts := operationPeerFixture(t, 20, func(w http.ResponseWriter, r *http.Request) {
		active.Add(1)
		defer active.Add(-1)
		started <- struct{}{}
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/operations", nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("peer read did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("caller request did not stop")
	}
	deadline := time.Now().Add(time.Second)
	for active.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if active.Load() != 0 {
		t.Fatal("caller cancellation left peer requests alive")
	}
}

// Synthetic peer latency only: exclude catalog discovery, local metrics and
// rendering. The serial reference is the previous per-node fetch algorithm.
func BenchmarkOperationsPeerFetch(b *testing.B) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond)
		_, _ = w.Write([]byte(`{"alarms":{}}`))
	}))
	defer peer.Close()
	s := &Server{opt: Options{Cluster: hub.NewCluster([]string{peer.URL}, "", nil)}, operationPeerSlots: make(chan struct{}, operationPeerConcurrency)}
	infos := make([]hub.Info, 16)
	views := make([]*view, len(infos))
	for i := range infos {
		infos[i] = hub.Info{ID: fmt.Sprintf("peer-%d", i), Peer: peer.URL}
	}
	b.Run("serial_reference", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			for _, inf := range infos {
				if _, err := s.peerAlarms(context.Background(), inf.Peer, inf.ID); err != nil {
					b.Fatal(err)
				}
			}
		}
	})
	b.Run("bounded_parallel", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			for _, result := range s.operationPeerAlarms(context.Background(), infos, views) {
				if !result.ok {
					b.Fatal("peer read failed")
				}
			}
		}
	})
}
