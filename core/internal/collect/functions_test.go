package collect

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

type busyFunctionCollector struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *busyFunctionCollector) Name() string                  { return "test-stable-functions" }
func (c *busyFunctionCollector) Init(*registry.Registry) error { return nil }
func (c *busyFunctionCollector) Collect(context.Context, *registry.Registry, time.Time) error {
	c.once.Do(func() { close(c.started) })
	<-c.release
	return nil
}
func (c *busyFunctionCollector) Functions() []Function {
	return []Function{{Name: "stable", Run: func(context.Context, map[string]string) (any, error) { return "ok", nil }}}
}

func TestFunctionsStayAvailableWhileCollectorIsBusy(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	Register("test-stable-functions", func() Collector {
		return &busyFunctionCollector{started: started, release: release}
	})
	defer func() {
		regMu.Lock()
		delete(factories, "test-stable-functions")
		regMu.Unlock()
	}()
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	s := NewScheduler(reg, slog.Default(), Options{Names: []string{"test-stable-functions"}})
	check := func(want int) {
		t.Helper()
		got := s.Functions()
		if len(got) != want {
			t.Fatalf("functions: got %d, want %d", len(got), want)
		}
		if want > 0 && got[0].Name != "stable" {
			t.Fatalf("wrong function: %q", got[0].Name)
		}
	}
	check(1)
	done := make(chan struct{})
	go func() { s.tick(context.Background(), time.Now()); close(done) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("collector did not start")
	}
	check(1) // Collect holds opMu, but the function must remain advertised.
	close(release)
	<-done
	if !s.SetEnabled("test-stable-functions", false) {
		t.Fatal("disable failed")
	}
	check(0)
	if !s.SetEnabled("test-stable-functions", true) {
		t.Fatal("enable failed")
	}
	s.tick(context.Background(), time.Now().Add(time.Second))
	s.initWG.Wait()
	check(1)
}
