package collect

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func initHTTPCheckForTest(t *testing.T, cfg httpcheckConfig) (*httpcheckCollector, *registry.Registry) {
	t.Helper()
	h := &httpcheckCollector{cfg: cfg}
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	if err := h.Init(reg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)
	return h, reg
}

func TestHTTPCheckReusesConnectionsAndClosesOnStop(t *testing.T) {
	var opened atomic.Int32
	closed := make(chan struct{}, 10)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
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
	h, reg := initHTTPCheckForTest(t, httpcheckConfig{Jobs: []httpJob{
		{Name: "first", URL: srv.URL}, {Name: "second", URL: srv.URL},
	}})
	for i := range 5 {
		if err := h.Collect(context.Background(), reg, time.Unix(1700000000+int64(i), 0)); err != nil {
			t.Fatal(err)
		}
	}
	if got := opened.Load(); got != 1 {
		t.Fatalf("10 probes opened %d connections, want one reused connection", got)
	}
	h.Stop()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop retained the idle connection")
	}
}

func TestHTTPCheckBodyReadFailuresAreNotHealthy(t *testing.T) {
	for _, tc := range []struct {
		name string
		wait bool
		want error
	}{
		{"timeout", true, context.DeadlineExceeded},
		{"truncated", false, io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "20")
				_, _ = io.WriteString(w, "abc")
				w.(http.Flusher).Flush()
				if tc.wait {
					<-r.Context().Done()
				}
			}))
			defer srv.Close()
			h, reg := initHTTPCheckForTest(t, httpcheckConfig{Jobs: []httpJob{{Name: "body", URL: srv.URL, Timeout: 200 * time.Millisecond}}})
			if err := h.Collect(context.Background(), reg, time.Now()); !errors.Is(err, tc.want) {
				t.Fatalf("body error = %v, want %v", err, tc.want)
			}
			for suffix, want := range map[string]map[string]float64{
				"status":          {"success": 0, "failure": 1},
				"status_code":     {"code": 200},
				"response_length": {"length": 3},
			} {
				ch, _ := reg.Chart("httpcheck." + suffix + ".body")
				_, got := ch.LastValues()
				for dimension, value := range want {
					if got[dimension] != value {
						t.Errorf("%s.%s = %v, want %v", suffix, dimension, got[dimension], value)
					}
				}
			}
		})
	}
}

func TestHTTPCheckTLSVerificationPoliciesStaySeparate(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	defer srv.Close()
	insecure := httpJob{Name: "insecure", URL: srv.URL}
	insecure.TLS.Insecure = true
	h, reg := initHTTPCheckForTest(t, httpcheckConfig{Jobs: []httpJob{
		insecure, {Name: "strict", URL: srv.URL},
	}})
	for range 2 {
		// The insecure job warms a pooled connection before the strict job.
		if err := h.Collect(context.Background(), reg, time.Now()); err != nil {
			t.Fatal(err)
		}
		for job, success := range map[string]float64{"insecure": 1, "strict": 0} {
			ch, _ := reg.Chart("httpcheck.status." + job)
			_, got := ch.LastValues()
			if got["success"] != success || got["failure"] != 1-success {
				t.Fatalf("%s used the other job's TLS policy: %v", job, got)
			}
		}
	}
}

func TestHTTPCheckPreservesPerJobRequestPolicy(t *testing.T) {
	var followed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/target" {
			followed.Store(true)
			return
		}
		if r.Method != http.MethodHead || r.Header.Get("X-Job") != "fixture" {
			t.Errorf("request policy changed: method=%s header=%q", r.Method, r.Header.Get("X-Job"))
		}
		// This exceeds the collector default but fits this job's deadline.
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Location", "/target")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	h, reg := initHTTPCheckForTest(t, httpcheckConfig{Timeout: time.Millisecond, Jobs: []httpJob{{
		Name: "custom", URL: srv.URL, Method: http.MethodHead, Headers: map[string]string{"X-Job": "fixture"},
		Timeout: time.Second, StatusOK: []int{http.StatusFound},
	}}})
	if err := h.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if followed.Load() {
		t.Fatal("probe followed a redirect")
	}
}

func TestHTTPCheckPreservesResponseLengthLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", (1<<20)+100))
	}))
	defer srv.Close()
	h, reg := initHTTPCheckForTest(t, httpcheckConfig{Jobs: []httpJob{{Name: "large", URL: srv.URL}}})
	if err := h.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	ch, _ := reg.Chart("httpcheck.response_length.large")
	_, got := ch.LastValues()
	if got["length"] != 1<<20 {
		t.Fatalf("response length = %v, want existing 1 MiB cap", got["length"])
	}
}
