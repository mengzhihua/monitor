package trapper

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func senderFrame(t *testing.T) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"request": "sender data", "psk": "test-only",
		"data": []map[string]any{{"host": "sender", "key": "load", "value": "1.25"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	frame := append([]byte(nil), header...)
	frame = binary.LittleEndian.AppendUint64(frame, uint64(len(payload)))
	return append(frame, payload...)
}

func startLifecycleServer(t *testing.T, ctx context.Context, sink registry.Sink) (*server, *registry.Registry) {
	t.Helper()
	reg := registry.New(&registry.Host{ID: "test", Hostname: "test", UpdateEvery: 1}, sink)
	listener, err := Start(ctx.Done(), "127.0.0.1:0", nil, "test-only", reg)
	if err != nil {
		t.Fatal(err)
	}
	s := listener.(*server)
	t.Cleanup(func() { _ = s.Close() })
	return s, reg
}

func connectSender(t *testing.T, s *server) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", s.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return conn
}

func waitAccepted(t *testing.T, s *server) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		accepted := len(s.connections) > 0
		s.mu.Unlock()
		if accepted {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("server did not accept sender")
}

func requireStopped(t *testing.T, s *server) {
	t.Helper()
	for name, done := range map[string]<-chan struct{}{"accept and handlers": s.done, "cancellation watcher": s.watchDone} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("%s did not stop", name)
		}
	}
	s.mu.Lock()
	active := len(s.connections)
	s.mu.Unlock()
	if active != 0 {
		t.Fatalf("%d connections retained after shutdown", active)
	}
	listener, err := net.Listen("tcp", s.Addr().String())
	if err != nil {
		t.Fatalf("cannot immediately reuse the listener address: %v", err)
	}
	_ = listener.Close()
}

func TestServerStopsIncompleteSender(t *testing.T) {
	for _, mode := range []string{"context", "explicit_close"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s, reg := startLifecycleServer(t, ctx, nil)
			conn := connectSender(t, s)
			frame := senderFrame(t)
			if _, err := conn.Write(frame[:len(frame)-1]); err != nil {
				t.Fatal(err)
			}
			waitAccepted(t, s)
			if mode == "context" {
				cancel()
			} else {
				closed := make(chan error, 1)
				go func() { closed <- s.Close() }()
				select {
				case err := <-closed:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("explicit Close waited for context cancellation or the five-second read timeout")
				}
				if ctx.Err() != nil {
					t.Fatal("explicit Close canceled the caller's context")
				}
			}
			requireStopped(t, s)
			var buf [1]byte
			if _, err := conn.Read(buf[:]); !errors.Is(err, io.EOF) {
				// Some platforms reset a socket with unread sender bytes.
				if netErr, ok := err.(net.Error); err == nil || (ok && netErr.Timeout()) {
					t.Fatalf("sender remains open after shutdown: %v", err)
				}
			}
			_, _ = conn.Write(frame[len(frame)-1:])
			if _, ok := reg.Chart("trapper.sender.load"); ok {
				t.Fatal("incomplete sender created a sample during shutdown")
			}
			if err := s.Close(); err != nil {
				t.Fatalf("repeated Close: %v", err)
			}
		})
	}
}

func TestServerAcceptsCompleteSender(t *testing.T) {
	s, reg := startLifecycleServer(t, context.Background(), nil)
	conn := connectSender(t, s)
	if _, err := conn.Write(senderFrame(t)); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(response, []byte(`"response":"success"`)) {
		t.Fatalf("sender response: %s", response)
	}
	chart, ok := reg.Chart("trapper.sender.load")
	if !ok {
		t.Fatal("sender sample was not stored")
	}
	_, values := chart.LastValues()
	if values["value"] != 1.25 {
		t.Fatalf("sender value: %v", values)
	}
}

type blockedSenderSink struct {
	entered chan struct{}
	release chan struct{}
	writes  atomic.Int32
}

func (s *blockedSenderSink) Append(string, int64, float64) {
	close(s.entered)
	<-s.release
	s.writes.Add(1)
}

func TestServerCloseJoinsAcceptedSample(t *testing.T) {
	sink := &blockedSenderSink{entered: make(chan struct{}), release: make(chan struct{})}
	s, _ := startLifecycleServer(t, context.Background(), sink)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(sink.release) }) }
	t.Cleanup(release)
	conn := connectSender(t, s)
	if _, err := conn.Write(senderFrame(t)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sink.entered:
	case <-time.After(time.Second):
		t.Fatal("sender did not reach the sample sink")
	}
	const callers = 4
	closed := make(chan error, callers)
	for range callers {
		go func() { closed <- s.Close() }()
	}
	select {
	case err := <-closed:
		t.Fatalf("Close returned before an accepted sample completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	release()
	for range callers {
		select {
		case err := <-closed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("Close did not join the completed sample")
		}
	}
	if writes := sink.writes.Load(); writes != 1 {
		t.Fatalf("completed samples: got %d, want 1", writes)
	}
	requireStopped(t, s)
}
