package stream

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func TestControlHTTPRequestsCloseConnections(t *testing.T) {
	for _, tc := range []struct {
		name, path, method, body string
		status                   int
		claim, wantError         bool
	}{
		{name: "config", path: "/api/v1/agent/config", method: http.MethodGet, body: `{"disabled":["apps"]}`, status: http.StatusOK},
		{name: "invalid config", path: "/api/v1/agent/config", method: http.MethodGet, body: `{invalid}`, status: http.StatusOK, wantError: true},
		{name: "claim", path: "/api/v1/claim", method: http.MethodPost, body: `{"api_key":"fixture-key"}`, status: http.StatusOK, claim: true},
		{name: "rejected claim", path: "/api/v1/claim", method: http.MethodPost, body: `claim rejected`, status: http.StatusForbidden, claim: true, wantError: true},
		{name: "invalid claim", path: "/api/v1/claim", method: http.MethodPost, body: `{invalid}`, status: http.StatusOK, claim: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const attempts = 3
			var opened atomic.Int32
			closed := make(chan struct{}, attempts)
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path || r.Method != tc.method {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateNew {
					opened.Add(1)
				} else if state == http.StateClosed {
					closed <- struct{}{}
				}
			}
			srv.Start()
			defer srv.Close()
			configured := 0
			client := &Client{opt: ClientOptions{APIKey: "fixture-key", ClaimToken: "fixture-claim", Timeout: time.Second,
				OnConfig: func(disabled []string) {
					configured++
					if !slices.Equal(disabled, []string{"apps"}) {
						t.Errorf("disabled = %v", disabled)
					}
				}}}
			for range attempts {
				if tc.claim {
					client.opt.APIKey = ""
					err := client.redeemClaim(context.Background(), srv.URL)
					if (err != nil) != tc.wantError {
						t.Fatalf("claim error = %v, wantError = %v", err, tc.wantError)
					}
					if !tc.wantError && client.opt.APIKey != "fixture-key" {
						t.Fatalf("claim did not save the returned key")
					}
				} else {
					client.fetchConfig(context.Background(), srv.URL, nil)
				}
			}
			if !tc.claim {
				want := attempts
				if tc.wantError {
					want = 0
				}
				if configured != want {
					t.Fatalf("config callbacks = %d, want %d", configured, want)
				}
			}
			deadline := time.NewTimer(2 * time.Second)
			defer deadline.Stop()
			for n := 0; n < attempts; n++ {
				select {
				case <-closed:
				case <-deadline.C:
					t.Fatalf("%d of %d connections closed after %d requests; idle connections remain", n, opened.Load(), attempts)
				}
			}
		})
	}
}
