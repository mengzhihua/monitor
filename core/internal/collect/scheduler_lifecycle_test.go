package collect

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

type lifecycleCollector struct {
	name      string
	configure func() error
	init      func() error
	collect   func(context.Context) error
	stop      func()
}

func (c *lifecycleCollector) Name() string { return c.name }
func (c *lifecycleCollector) Configure(func(any) error) error {
	if c.configure != nil {
		return c.configure()
	}
	return nil
}
func (c *lifecycleCollector) Init(*registry.Registry) error {
	if c.init != nil {
		return c.init()
	}
	return nil
}
func (c *lifecycleCollector) Collect(ctx context.Context, _ *registry.Registry, _ time.Time) error {
	if c.collect != nil {
		return c.collect(ctx)
	}
	return nil
}
func (c *lifecycleCollector) Stop() {
	if c.stop != nil {
		c.stop()
	}
}

func registerLifecycleCollector(t *testing.T, name string, f Factory) {
	t.Helper()
	regMu.Lock()
	previous, existed := factories[name]
	factories[name] = f
	regMu.Unlock()
	t.Cleanup(func() {
		regMu.Lock()
		defer regMu.Unlock()
		if existed {
			factories[name] = previous
		} else {
			delete(factories, name)
		}
	})
}

func awaitScheduler(t *testing.T, done <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal(message)
	}
}

func TestSchedulerConstructionUsesCatalogSnapshot(t *testing.T) {
	const first, second = "lifecycle-first", "lifecycle-second"
	var stages atomic.Int32
	original := &lifecycleCollector{name: second}
	replacement := &lifecycleCollector{name: second}
	registerLifecycleCollector(t, second, func() Collector { return original })
	registerLifecycleCollector(t, first, func() Collector {
		// Each extension callback may read or update the global catalog. The
		// selected second factory must still come from the original snapshot.
		_ = Available()
		Register(second, func() Collector { return replacement })
		stages.Add(1)
		return &lifecycleCollector{name: first, configure: func() error {
			_ = Available()
			stages.Add(1)
			return nil
		}, init: func() error {
			Register(second, func() Collector { return replacement })
			stages.Add(1)
			return nil
		}}
	})
	var s *Scheduler
	done := make(chan struct{})
	go func() {
		s = NewScheduler(registry.New(&registry.Host{UpdateEvery: 1}, nil), nil, Options{Names: []string{first, second}})
		close(done)
	}()
	awaitScheduler(t, done, "collector callbacks deadlocked against the catalog lock")
	defer s.Close()
	if stages.Load() != 3 || s.Collector(second) != original {
		t.Fatal("constructor did not use one catalog snapshot across extension callbacks")
	}
}

func TestSchedulerCloseBeforeRunReleasesListenerPorts(t *testing.T) {
	// These listeners are acquired by NewScheduler. Closing must also work
	// when a later daemon setup step fails and Run is never entered.
	s := NewScheduler(testReg(t), nil, Options{
		Names: []string{"statsd", "otlp"},
		Modules: map[string]func(any) error{
			"statsd": func(v any) error { v.(*statsdConfig).Listen = "127.0.0.1:0"; return nil },
			"otlp":   func(v any) error { v.(*otlpConfig).Listen = "127.0.0.1:0"; return nil },
		},
	})
	defer s.Close()
	for _, status := range s.Status() {
		if !status.Enabled {
			t.Fatalf("listener initialization failed: %+v", status)
		}
	}
	udpAddr := s.Collector("statsd").(*statsdCollector).conn.LocalAddr().String()
	tcpAddr := s.Collector("otlp").(*otlpCollector).Addr()
	assertSchedulerPortBound(t, "udp", udpAddr)
	assertSchedulerPortBound(t, "tcp", tcpAddr)
	var closes sync.WaitGroup
	for range 8 {
		closes.Add(1)
		go func() { defer closes.Done(); s.Close() }()
	}
	closes.Wait()
	assertSchedulerStopped(t, s.Collector("statsd").(*statsdCollector).done, "StatsD reader")
	assertSchedulerStopped(t, s.Collector("otlp").(*otlpCollector).done, "OTLP server")
	assertSchedulerPortReleased(t, "udp", udpAddr)
	assertSchedulerPortReleased(t, "tcp", tcpAddr)
	if s.SetEnabled("statsd", true) {
		t.Fatal("closed scheduler accepted a new enable request")
	}
	s.Run(context.Background()) // Close is terminal, even for a later Run.
	for _, status := range s.Status() {
		if status.Enabled || status.Runs != 0 {
			t.Fatalf("closed scheduler ran collector: %+v", status)
		}
	}
}

func TestSchedulerCloseStopsIncompleteOTLPRequest(t *testing.T) {
	s := NewScheduler(testReg(t), nil, Options{Names: []string{"otlp"}, Modules: map[string]func(any) error{
		"otlp": func(v any) error { v.(*otlpConfig).Listen = "127.0.0.1:0"; return nil },
	}})
	defer s.Close()
	collector := s.Collector("otlp").(*otlpCollector)
	conn, err := net.DialTimeout("tcp", collector.Addr(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// Receiving 100 Continue proves the real handler entered its body read.
	// Leave that body incomplete so graceful shutdown must force the socket
	// closed, then join the handler before the registry owner can close its DB.
	_, err = fmt.Fprint(conn, "POST /v1/metrics HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 100\r\nExpect: 100-continue\r\n\r\n{")
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusContinue {
		t.Fatalf("handler did not start reading body: %s", response.Status)
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	awaitScheduler(t, closed, "Close did not force the incomplete OTLP request to end")
	if _, err := io.ReadAll(reader); err != nil {
		if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			t.Fatal("OTLP client socket remained open after Close")
		}
	}
	assertSchedulerStopped(t, collector.done, "OTLP server")
	assertSchedulerPortReleased(t, "tcp", collector.Addr())
}

type partialInitCollector struct {
	Collector
	stops atomic.Int32
}

func (c *partialInitCollector) Init(reg *registry.Registry) error {
	if err := c.Collector.Init(reg); err != nil {
		return err
	}
	return errors.New("failure after acquiring listener")
}
func (c *partialInitCollector) Stop() {
	c.stops.Add(1)
	c.Collector.(Stopper).Stop()
}

func TestSchedulerFailedInitReleasesListenerPortsOnce(t *testing.T) {
	for _, name := range []string{"statsd", "otlp"} {
		t.Run(name, func(t *testing.T) {
			var inner Collector
			if name == "statsd" {
				inner = &statsdCollector{cfg: statsdConfig{Listen: "127.0.0.1:0"}}
			} else {
				inner = &otlpCollector{cfg: otlpConfig{Listen: "127.0.0.1:0"}}
			}
			c := &partialInitCollector{Collector: inner}
			const id = "lifecycle-partial-init"
			registerLifecycleCollector(t, id, func() Collector { return c })
			s := NewScheduler(testReg(t), nil, Options{Names: []string{id}})
			defer s.Close()
			if status := s.Status()[0]; status.Enabled || status.Error != "failure after acquiring listener" {
				t.Fatalf("unexpected failed initialization: %+v", status)
			}
			if name == "statsd" {
				assertSchedulerPortReleased(t, "udp", inner.(*statsdCollector).conn.LocalAddr().String())
			} else {
				assertSchedulerPortReleased(t, "tcp", inner.(*otlpCollector).Addr())
			}
			s.Close()
			if c.stops.Load() != 1 {
				t.Fatalf("failed instance stopped %d times, want once", c.stops.Load())
			}
		})
	}
}

func TestSchedulerShutdownCancelsCollectionBeforeStop(t *testing.T) {
	for _, method := range []string{"cancel-run", "explicit-close"} {
		t.Run(method, func(t *testing.T) {
			started, collected := make(chan struct{}), make(chan struct{})
			var stops atomic.Int32
			const name = "lifecycle-running"
			registerLifecycleCollector(t, name, func() Collector {
				return &lifecycleCollector{name: name, collect: func(ctx context.Context) error {
					close(started)
					<-ctx.Done()
					close(collected)
					return ctx.Err()
				}, stop: func() {
					select {
					case <-collected:
					default:
						t.Error("Stop overlapped an active Collect")
					}
					stops.Add(1)
				}}
			})
			s := NewScheduler(testReg(t), nil, Options{Names: []string{name}, Timeout: time.Minute})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			defer s.Close()
			done := make(chan struct{})
			go func() { s.Run(ctx); close(done) }()
			awaitScheduler(t, started, "collector did not start")
			if method == "cancel-run" {
				cancel()
			} else {
				closed := make(chan struct{})
				go func() { s.Close(); close(closed) }()
				awaitScheduler(t, closed, "Close did not cancel the active Run")
				if ctx.Err() != nil {
					t.Fatal("Close unexpectedly cancelled the caller context")
				}
			}
			awaitScheduler(t, done, "Run did not complete shutdown")
			s.Close()
			if stops.Load() != 1 {
				t.Fatalf("collector stopped %d times, want once", stops.Load())
			}
		})
	}
}

func TestSchedulerCloseWaitsForRetryInit(t *testing.T) {
	const name = "lifecycle-retry"
	entered, release := make(chan struct{}), make(chan struct{})
	var made, stops atomic.Int32
	var ln net.Listener
	registerLifecycleCollector(t, name, func() Collector {
		attempt := made.Add(1)
		return &lifecycleCollector{name: name, init: func() error {
			if attempt == 1 {
				return errors.New("dependency temporarily unavailable")
			}
			var err error
			ln, err = net.Listen("tcp", "127.0.0.1:0")
			close(entered)
			<-release
			return err
		}, stop: func() {
			stops.Add(1)
			if attempt > 1 && ln != nil {
				_ = ln.Close()
			}
		}}
	})
	s := NewScheduler(testReg(t), nil, Options{Names: []string{name}})
	defer s.Close()
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	s.tick(context.Background(), time.Now().Add(time.Minute))
	awaitScheduler(t, entered, "retry did not enter Init")
	if ln == nil {
		t.Fatal("retry did not acquire a listener")
	}
	addr := ln.Addr().String()
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	awaitScheduler(t, s.lifecycle().Done(), "Close did not begin shutdown")
	select {
	case <-closed:
		t.Fatal("Close returned before in-flight Init completed")
	default:
	}
	// Further ticks cannot start new retries while shutdown joins this one.
	for range 10 {
		s.tick(context.Background(), time.Now().Add(time.Hour))
	}
	releaseOnce.Do(func() { close(release) })
	awaitScheduler(t, closed, "Close did not join retry cleanup")
	assertSchedulerPortReleased(t, "tcp", addr)
	if made.Load() != 2 || stops.Load() != 2 {
		t.Fatalf("instances=%d stops=%d, want exactly two cleaned instances", made.Load(), stops.Load())
	}
	if status := s.Status()[0]; status.Enabled {
		t.Fatalf("closed retry remained enabled: %+v", status)
	}
}

func assertSchedulerPortBound(t *testing.T, network, addr string) {
	t.Helper()
	closer, err := listenSchedulerPort(network, addr)
	if err == nil {
		closer()
		t.Fatalf("%s listener %s was not bound", network, addr)
	}
}

func assertSchedulerStopped(t *testing.T, done <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-done:
	default:
		t.Fatalf("%s remained active after Close", name)
	}
}

func assertSchedulerPortReleased(t *testing.T, network, addr string) {
	t.Helper()
	closer, err := listenSchedulerPort(network, addr)
	if err != nil {
		t.Fatalf("%s listener %s was not released: %v", network, addr, err)
	}
	closer()
}

func listenSchedulerPort(network, addr string) (func(), error) {
	if network == "udp" {
		conn, err := net.ListenPacket(network, addr)
		if err != nil {
			return nil, err
		}
		return func() { _ = conn.Close() }, nil
	}
	ln, err := net.Listen(network, addr)
	if err != nil {
		return nil, err
	}
	return func() { _ = ln.Close() }, nil
}
