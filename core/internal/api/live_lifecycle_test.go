package api

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"weak"

	"github.com/gorilla/websocket"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

func assertLiveClientClosed(t *testing.T, ws *websocket.Conn) {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(time.Second))
	for {
		if _, _, err := ws.ReadMessage(); err != nil {
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				t.Fatal("live socket remained open after shutdown")
			}
			return
		}
	}
}

func TestServerCloseDisconnectsLiveClientsAndRejectsUpgrades(t *testing.T) {
	s := newLifecycleTestServer(t, Options{}, nil)
	s.reg.AddChart(&registry.Chart{ID: "live.test", Dimensions: []*registry.Dimension{{ID: "value"}}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/live"
	var clients []*websocket.Conn
	for range 4 {
		ws, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer ws.Close()
		clients = append(clients, ws)
	}
	collected := make(chan struct{})
	go func() {
		defer close(collected)
		for i := range 300 {
			_ = s.reg.Collect("live.test", time.Unix(int64(i+1), 0), map[string]float64{"value": float64(i)})
		}
	}()
	closed := make(chan struct{})
	go func() {
		var closers sync.WaitGroup
		for range 8 {
			closers.Go(s.Close)
		}
		closers.Wait()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Server.Close did not join live connections")
	}
	<-collected
	for _, ws := range clients {
		assertLiveClientClosed(t, ws)
	}
	s.live.mu.RLock()
	remaining := len(s.live.conns)
	s.live.mu.RUnlock()
	if remaining != 0 {
		t.Fatalf("Close retained %d live connections", remaining)
	}
	ws, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if ws != nil {
		ws.Close()
	}
	if resp != nil {
		defer resp.Body.Close()
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("upgrade after close: response=%v err=%v", resp, err)
	}
}

type gatedHijacker struct {
	http.ResponseWriter
	entered chan struct{}
	release <-chan struct{}
}

func (w gatedHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	close(w.entered)
	<-w.release
	return w.ResponseWriter.(http.Hijacker).Hijack()
}

func TestLiveCloseWaitsForInFlightUpgrade(t *testing.T) {
	reg := registry.New(&registry.Host{ID: "live"}, nil)
	h := newLiveHub(reg, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.handle(gatedHijacker{w, entered, release}, r, "", nil, nil)
	}))
	defer ts.Close()
	defer h.close()
	defer releaseOnce.Do(func() { close(release) })
	type dialResult struct {
		ws  *websocket.Conn
		err error
	}
	dialed := make(chan dialResult, 1)
	go func() {
		ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http"), nil)
		dialed <- dialResult{ws, err}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("upgrade did not reach hijack")
	}
	closed := make(chan struct{})
	go func() { h.close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close returned before an in-flight upgrade finished")
	case <-time.After(20 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not join the finished upgrade")
	}
	result := <-dialed
	if result.ws != nil {
		defer result.ws.Close()
		assertLiveClientClosed(t, result.ws)
	} else if result.err == nil {
		t.Fatal("dial returned no connection or error")
	}
}

func TestLiveCloseJoinsWriter(t *testing.T) {
	reg := registry.New(&registry.Host{ID: "live"}, nil)
	h := newLiveHub(reg, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.handle(w, r, "", nil, func(string) bool {
			close(entered)
			<-release
			return true
		})
	}))
	defer ts.Close()
	defer h.close()
	defer releaseOnce.Do(func() { close(release) })
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	deadline := time.Now().Add(time.Second)
	queued := false
	for time.Now().Before(deadline) && !queued {
		h.mu.RLock()
		for c := range h.conns {
			c.send <- liveFrame{payload: []byte(`{"chart":"x"}`)}
			queued = true
		}
		h.mu.RUnlock()
		if !queued {
			time.Sleep(time.Millisecond)
		}
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("writer did not start")
	}
	closed := make(chan struct{})
	go func() { h.close(); close(closed) }()
	assertLiveClientClosed(t, ws)
	select {
	case <-closed:
		t.Fatal("Close returned before the writer exited")
	case <-time.After(20 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not join the released writer")
	}
}

// A live subscription is the only registry-owned reference to this hub. Weak
// pointers verify repeated Server reconstruction actually releases it.
func closedServerHub(t *testing.T, owner *Server) weak.Pointer[liveHub] {
	t.Helper()
	s, err := New(owner.reg, owner.db, owner.sched, Options{})
	if err != nil {
		t.Fatal(err)
	}
	h := weak.Make(s.live)
	s.Close()
	return h
}

func TestRecreatedServersReleaseLiveSubscriptions(t *testing.T) {
	owner := newLifecycleTestServer(t, Options{}, nil)
	var hubs []weak.Pointer[liveHub]
	for range 20 {
		hubs = append(hubs, closedServerHub(t, owner))
	}
	for range 5 {
		runtime.GC()
		retained := 0
		for _, h := range hubs {
			if h.Value() != nil {
				retained++
			}
		}
		if retained == 0 {
			return
		}
	}
	t.Fatal("registry retained closed API live hubs")
}
