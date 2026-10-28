package hub

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/stream"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func TestAgentFunctionCallsBelongToStreamSession(t *testing.T) {
	for _, protocol := range []string{"stream", "mqtt"} {
		t.Run(protocol, func(t *testing.T) {
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir(), Tiers: []tsdb.TierSpec{}, Logger: log})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			nodes, err := Open(db, t.TempDir(), Options{Keys: []string{"session-fixture"}, Logger: log})
			if err != nil {
				t.Fatal(err)
			}
			handler := nodes.HandleStream
			if protocol == "mqtt" {
				handler = nodes.HandleACLK
			}
			server := httptest.NewServer(http.HandlerFunc(handler))
			defer server.Close()
			started, canceled := make(chan struct{}), make(chan struct{})
			var active atomic.Bool
			functions := func() []collect.Function {
				return []collect.Function{{Name: "plugin.demo.serial", Timeout: 30,
					Run: func(ctx context.Context, args map[string]string) (any, error) {
						if !active.CompareAndSwap(false, true) {
							return nil, errors.New("previous session still holds the call slot")
						}
						defer func() {
							active.Store(false)
							if args["block"] == "yes" {
								close(canceled)
							}
						}()
						if args["block"] == "yes" {
							close(started)
							<-ctx.Done()
							return nil, ctx.Err()
						}
						return "reconnected", nil
					}}}
			}
			reg := registry.New(&registry.Host{ID: "session-agent", Hostname: "session-agent"}, nil)
			client := stream.NewClient(reg, nil, stream.ClientOptions{Destinations: []string{server.URL}, APIKey: "session-fixture",
				Protocol: protocol, Timeout: 3 * time.Second, Logger: log, Functions: functions})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); client.Run(ctx) }()
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("client did not stop")
				}
			}()
			waitSession := func(previous *session) (*Node, *session) {
				t.Helper()
				deadline := time.Now().Add(8 * time.Second)
				for time.Now().Before(deadline) {
					if node, ok := nodes.Get("session-agent"); ok {
						node.mu.Lock()
						current := node.conn
						node.mu.Unlock()
						if current != nil && current != previous && client.Status().Connected {
							return node, current
						}
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatal("agent did not establish a new stream session")
				return nil, nil
			}
			node, firstSession := waitSession(nil)
			callCtx, stopCall := context.WithTimeout(ctx, 15*time.Second)
			defer stopCall()
			callDone := make(chan error, 1)
			go func() {
				_, err := node.Call(callCtx, "plugin.demo.serial", map[string]string{"block": "yes"})
				callDone <- err
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("function did not start")
			}
			firstSession.close(errors.New("fixture disconnect"))
			select {
			case <-canceled:
			case <-time.After(3 * time.Second):
				t.Fatal("disconnected session left its function running")
			}
			select {
			case err := <-callDone:
				if err == nil {
					t.Fatal("disconnected RPC succeeded")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("hub retained disconnected RPC until its request deadline")
			}
			if ctx.Err() != nil {
				t.Fatal("session cancellation escaped into the client lifetime")
			}
			node, _ = waitSession(firstSession)
			retryCtx, stopRetry := context.WithTimeout(ctx, 3*time.Second)
			defer stopRetry()
			result, err := node.Call(retryCtx, "plugin.demo.serial", nil)
			if err != nil || string(result) != `"reconnected"` {
				t.Fatalf("new session could not call after old cancellation: %s %v", result, err)
			}
		})
	}
}
