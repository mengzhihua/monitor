package stream

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestFunctionCatalogRequiresWelcomeCapability(t *testing.T) {
	for _, supported := range []bool{false, true} {
		name := "legacy hub"
		if supported {
			name = "capable hub"
		}
		t.Run(name, func(t *testing.T) {
			var changed atomic.Bool
			frames := make(chan Frame, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upgrader := websocket.Upgrader{}
				ws, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer ws.Close()
				var hello Frame
				if err := ws.ReadJSON(&hello); err != nil {
					return
				}
				frames <- hello
				changed.Store(true)
				welcome := Frame{Type: TypeWelcome}
				if supported {
					welcome.Capabilities = []string{FunctionCatalogCapability}
				}
				if err := ws.WriteJSON(welcome); err != nil {
					return
				}
				for {
					var frame Frame
					if err := ws.ReadJSON(&frame); err != nil {
						return
					}
					frames <- frame
				}
			}))
			defer server.Close()
			reg := registry.New(&registry.Host{ID: "functions", Hostname: "functions"}, nil)
			client := NewClient(reg, nil, ClientOptions{Destinations: []string{server.URL}, Timeout: time.Second,
				Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
				Functions: func() []collect.Function {
					if changed.Load() {
						return []collect.Function{{Name: "plugin.demo.late"}}
					}
					return []collect.Function{{Name: "initial"}}
				}})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); client.Run(ctx) }()
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("stream client did not stop")
				}
			}()
			select {
			case hello := <-frames:
				if hello.Type != TypeHello || len(hello.Functions) != 1 || hello.Functions[0].Name != "initial" {
					t.Fatalf("invalid hello catalog: %+v", hello)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("hello not received")
			}
			// Observe more than a catalog period. Legacy peers must never receive
			// the new frame; capable peers receive the late registration in place.
			wait := 1500 * time.Millisecond
			if supported {
				wait = 5 * time.Second
			}
			select {
			case frame := <-frames:
				if !supported || frame.Type != TypeFunctions || len(frame.Functions) != 1 || frame.Functions[0].Name != "plugin.demo.late" {
					t.Fatalf("unexpected catalog update: %+v", frame)
				}
			case <-time.After(wait):
				if supported {
					t.Fatal("negotiated catalog update not received")
				}
			}
		})
	}
}

func TestFunctionCatalogIgnoresProviderOrder(t *testing.T) {
	functions := []collect.Function{{Name: "z", Help: "last", Timeout: 2}, {Name: "a", Help: "first", Timeout: 3}}
	client := &Client{opt: ClientOptions{Functions: func() []collect.Function { return functions }}}
	first := client.functions()
	slices.Reverse(functions)
	if !slices.Equal(first, client.functions()) {
		t.Fatal("provider ordering must not generate redundant catalog updates")
	}
	functions[0].Help = "updated help"
	if slices.Equal(first, client.functions()) {
		t.Fatal("metadata change must generate an update")
	}
}
