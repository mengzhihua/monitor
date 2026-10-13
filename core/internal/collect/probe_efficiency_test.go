package collect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

type probeTransport func(*http.Request) (*http.Response, error)

func (f probeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func BenchmarkHTTPCheckResponseCounting(b *testing.B) {
	for _, size := range []int{1024, 1 << 20} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			payload := strings.Repeat("x", size)
			h := &httpcheckCollector{client: &http.Client{Transport: probeTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
			})}}
			job := httpJob{URL: "http://probe.invalid", Timeout: time.Second, StatusOK: []int{200}}
			b.ReportAllocs()
			b.SetBytes(int64(size))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ok, n, _, _, err := h.do(context.Background(), job)
				if err != nil || !ok || n != size {
					b.Fatalf("ok=%v n=%d err=%v", ok, n, err)
				}
			}
		})
	}
}

func TestCancelledSyntheticChecksDoNotPublishFailures(t *testing.T) {
	checks := []Collector{
		&httpcheckCollector{cfg: httpcheckConfig{Jobs: []httpJob{{Name: "cancelled", URL: "http://127.0.0.1:1"}}}},
		&portcheckCollector{cfg: portcheckConfig{Jobs: []portJob{{Name: "cancelled", Port: 1}}}},
		&dnsqueryCollector{cfg: dnsqueryConfig{Jobs: []dnsJob{{Name: "cancelled", Record: "127.0.0.1"}}}},
	}
	for _, c := range checks {
		t.Run(c.Name(), func(t *testing.T) {
			reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
			if err := c.Init(reg); err != nil {
				t.Fatal(err)
			}
			if stop, ok := c.(interface{ Stop() }); ok {
				defer stop.Stop()
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := c.Collect(ctx, reg, time.Now()); !errors.Is(err, context.Canceled) {
				t.Fatalf("expected cancellation, got %v", err)
			}
			for _, chart := range reg.Charts() {
				at, vals := chart.LatestValues()
				if at != 0 || len(vals) != 0 {
					t.Fatalf("cancelled check fabricated sample: %s %d %v", chart.ID, at, vals)
				}
			}
		})
	}
}

func TestHTTPCheckCancellationStopsRemainingJobs(t *testing.T) {
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	h := &httpcheckCollector{cfg: httpcheckConfig{Jobs: []httpJob{{Name: "first", URL: "http://probe.invalid/1"}, {Name: "second", URL: "http://probe.invalid/2"}, {Name: "third", URL: "http://probe.invalid/3"}}}}
	if err := h.Init(reg); err != nil {
		t.Fatal(err)
	}
	defer h.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	h.client.Transport = probeTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 2 {
			cancel()
			return nil, ctx.Err()
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	if err := h.Collect(ctx, reg, time.Unix(123, 0)); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if calls != 2 {
		t.Fatalf("started %d requests", calls)
	}
	for _, name := range []string{"first", "second", "third"} {
		ch, _ := reg.Chart("httpcheck.status." + name)
		at, vals := ch.LatestValues()
		if name == "first" {
			if at != 123 || vals["success"] != 1 {
				t.Fatal("lost completed sample")
			}
		} else if at != 0 {
			t.Fatalf("published cancelled job %s: %v", name, vals)
		}
	}
}

func TestHTTPCheckResponseCountRemainsBounded(t *testing.T) {
	for _, size := range []int{0, 23, (1 << 20) + 500} {
		h := &httpcheckCollector{client: &http.Client{Transport: probeTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(strings.Repeat("x", size)))}, nil
		})}}
		ok, n, _, _, err := h.do(context.Background(), httpJob{URL: "http://probe.invalid", Timeout: time.Second, StatusOK: []int{200}})
		if !ok || err != nil || n != min(size, 1<<20) {
			t.Fatalf("size=%d n=%d ok=%v err=%v", size, n, ok, err)
		}
	}
}

func TestDNSCheckCancellationDuringLookup(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	received := make(chan struct{}, 1)
	go func() {
		var buf [4096]byte
		for {
			if _, _, err := conn.ReadFrom(buf[:]); err != nil {
				return
			}
			select {
			case received <- struct{}{}:
			default:
			}
		}
	}()
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	d := &dnsqueryCollector{cfg: dnsqueryConfig{Jobs: []dnsJob{{Name: "waiting", Server: conn.LocalAddr().String(), Record: "probe.invalid", Timeout: time.Minute}}}}
	if err := d.Init(reg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Collect(ctx, reg, time.Now()) }()
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("no DNS query received")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled DNS lookup did not return")
	}
	for _, c := range reg.Charts() {
		at, _ := c.LatestValues()
		if at != 0 {
			t.Fatalf("cancelled lookup published %s", c.ID)
		}
	}
}
