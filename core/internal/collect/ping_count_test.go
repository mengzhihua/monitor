package collect

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestPingCountReportsLossAndAverageRTT(t *testing.T) {
	type result struct {
		rtt float64
		err error
	}
	failed := errors.New("probe failed")
	for _, tc := range []struct {
		name     string
		results  []result
		received float64
		average  float64
	}{
		// Windows CI observed a successful connection within the measured clock
		// resolution. Success is determined by the error, not a positive RTT.
		{"zero_duration_success", []result{{rtt: 0}}, 1, 0},
		{"sub_millisecond_successes", []result{{rtt: 0.125}, {rtt: 0.375}}, 2, 0.25},
		{"zero_and_positive_successes", []result{{rtt: 0}, {rtt: 6}}, 2, 3},
		{"partial_loss", []result{{rtt: 1.5}, {rtt: 99, err: failed}, {rtt: 4.5}}, 2, 3},
		{"one_success", []result{{rtt: 2}, {err: failed}, {err: failed}}, 1, 2},
		{"all_lost", []result{{err: failed}, {err: failed}, {err: failed}}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
			calls := 0
			p := &pingCollector{probeFn: func(_ context.Context, job pingJob, seq uint16) (float64, error) {
				if calls >= len(tc.results) {
					t.Fatal("collected more probes than configured")
				}
				if job.Name != "fixture" || seq != uint16(calls+1) {
					t.Fatalf("unexpected job %q or sequence %d", job.Name, seq)
				}
				result := tc.results[calls]
				calls++
				return result.rtt, result.err
			}}
			if err := p.Configure(func(v any) error {
				*v.(*pingConfig) = pingConfig{Jobs: []pingJob{{Name: "fixture", Host: "fixture.invalid", Count: len(tc.results)}}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := p.Init(reg); err != nil {
				t.Fatal(err)
			}
			if err := p.Collect(context.Background(), reg, time.Unix(40, 0)); err != nil {
				t.Fatal(err)
			}
			if calls != len(tc.results) {
				t.Fatalf("ran %d probes, want %d", calls, len(tc.results))
			}
			_, loss := mustChart(t, reg, "ping.loss.fixture").LastValues()
			if loss["received"] != tc.received || loss["dropped"] != float64(len(tc.results))-tc.received {
				t.Fatalf("loss %+v", loss)
			}
			_, latency := mustChart(t, reg, "ping.latency.fixture").LastValues()
			if latency["rtt"] != tc.average {
				t.Fatalf("RTT %v, want %v", latency["rtt"], tc.average)
			}
		})
	}
}

func TestPingTCPProbeReportsConnectivity(t *testing.T) {
	host, port, closeServer := servePingTCP(t)
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	p := &pingCollector{}
	if err := p.Configure(func(v any) error {
		*v.(*pingConfig) = pingConfig{Jobs: []pingJob{{Name: "tcp", Host: host, Protocol: "tcp", Port: port, Count: 3, Timeout: 2 * time.Second}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := p.Collect(context.Background(), reg, time.Unix(40, 0)); err != nil {
		t.Fatal(err)
	}
	_, loss := mustChart(t, reg, "ping.loss.tcp").LastValues()
	if loss["received"] != 3 || loss["dropped"] != 0 {
		t.Fatalf("open loss %+v", loss)
	}
	_, latency := mustChart(t, reg, "ping.latency.tcp").LastValues()
	if rtt := latency["rtt"]; rtt < 0 || math.IsNaN(rtt) || math.IsInf(rtt, 0) {
		t.Fatalf("open RTT %+v", latency)
	}

	// Stop our own ephemeral listener, not a presumed-unused system port. Do
	// not infer accept counts from handshake completion: those are asynchronous.
	closeServer()
	p.cfg.Jobs[0].Timeout = 100 * time.Millisecond
	if err := p.Collect(context.Background(), reg, time.Unix(41, 0)); err != nil {
		t.Fatal(err)
	}
	_, loss = mustChart(t, reg, "ping.loss.tcp").LastValues()
	_, latency = mustChart(t, reg, "ping.latency.tcp").LastValues()
	if loss["received"] != 0 || loss["dropped"] != 3 || latency["rtt"] != 0 {
		t.Fatalf("closed loss %+v RTT %+v", loss, latency)
	}
}

func TestPingCountCancellation(t *testing.T) {
	for _, beforeFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("before_first_%t", beforeFirst), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if beforeFirst {
				cancel()
			}
			calls := 0
			p := &pingCollector{cfg: pingConfig{Jobs: []pingJob{{Name: "cancel", Host: "fixture.invalid", Count: 3}}},
				probeFn: func(context.Context, pingJob, uint16) (float64, error) {
					calls++
					cancel()
					return 2, nil
				}}
			reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
			if err := p.Init(reg); err != nil {
				t.Fatal(err)
			}
			if err := p.Collect(ctx, reg, time.Unix(40, 0)); err != nil {
				t.Fatal(err)
			}
			wantCalls, wantRTT := 1, 2.0
			if beforeFirst {
				wantCalls, wantRTT = 0, 0
			}
			_, loss := mustChart(t, reg, "ping.loss.cancel").LastValues()
			_, latency := mustChart(t, reg, "ping.latency.cancel").LastValues()
			if calls != wantCalls || loss["received"] != float64(wantCalls) || loss["dropped"] != float64(3-wantCalls) || latency["rtt"] != wantRTT {
				t.Fatalf("calls=%d loss=%+v RTT=%+v", calls, loss, latency)
			}
		})
	}
}

func TestPingCountConfiguration(t *testing.T) {
	for _, count := range []int{-1, 0, 1, 5, 6} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			p := &pingCollector{}
			err := p.Configure(func(v any) error {
				*v.(*pingConfig) = pingConfig{Jobs: []pingJob{{Name: "gw", Host: "127.0.0.1", Count: count}}}
				return nil
			})
			if count < 0 || count > 5 {
				if err == nil {
					t.Fatalf("accepted count %d", count)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := count
			if want == 0 {
				want = 1
			}
			if p.cfg.Jobs[0].Count != want {
				t.Fatalf("count %d, want %d", p.cfg.Jobs[0].Count, want)
			}
		})
	}
}

func servePingTCP(t *testing.T) (host string, port int, closeFn func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	var once sync.Once
	closeFn = func() {
		once.Do(func() { _ = ln.Close() })
		<-done
	}
	t.Cleanup(closeFn)
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port, closeFn
}
