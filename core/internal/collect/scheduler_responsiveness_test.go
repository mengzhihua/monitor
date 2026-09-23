package collect

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

type gatedLifecycleCollector struct {
	stage   string
	started chan struct{}
	release chan struct{}
	initErr error
}

func (c *gatedLifecycleCollector) wait(stage string) {
	if c.stage == stage {
		close(c.started)
		<-c.release
	}
}

func (*gatedLifecycleCollector) Name() string { return "gated-lifecycle" }
func (c *gatedLifecycleCollector) Configure(func(any) error) error {
	c.wait("configure")
	return nil
}
func (c *gatedLifecycleCollector) Init(*registry.Registry) error {
	c.wait("init")
	return c.initErr
}
func (*gatedLifecycleCollector) Collect(context.Context, *registry.Registry, time.Time) error {
	return nil
}
func (c *gatedLifecycleCollector) Stop() { c.wait("stop") }
func (c *gatedLifecycleCollector) Functions() []Function {
	c.wait("functions")
	return []Function{{Name: "probe"}}
}

// A failed dependency may take seconds to probe or shut down. Those operations
// must not prevent /info or /functions from reading the completed state.
func TestSchedulerMetadataDoesNotWaitForRetry(t *testing.T) {
	for _, stage := range []string{"configure", "init", "functions", "stop"} {
		t.Run(stage, func(t *testing.T) {
			c := &gatedLifecycleCollector{stage: stage, started: make(chan struct{}), release: make(chan struct{})}
			if stage == "stop" {
				c.initErr = errors.New("retry failed")
			}
			r := &running{c: &gatedLifecycleCollector{}, factory: func() Collector { return c }, desired: true,
				status: Status{Name: c.Name(), Error: "previous failure"}, backoff: 5 * time.Second}
			s := &Scheduler{reg: registry.New(&registry.Host{UpdateEvery: 1}, nil), log: slog.Default(), cols: []*running{r}}
			done := make(chan struct{})
			go func() {
				r.opMu.Lock()
				s.prepare(r, time.Now())
				r.opMu.Unlock()
				close(done)
			}()
			select {
			case <-c.started:
			case <-time.After(2 * time.Second):
				close(c.release)
				t.Fatal("retry did not reach gated operation")
			}
			read := make(chan struct{})
			var status []Status
			var functions []Function
			go func() { status, functions = s.Status(), s.Functions(); close(read) }()
			select {
			case <-read:
				if len(status) != 1 || status[0].Enabled || status[0].Error != "previous failure" || len(functions) != 0 {
					t.Errorf("retry exposed unfinished state: status=%+v functions=%+v", status, functions)
				}
			case <-time.After(250 * time.Millisecond):
				t.Error("metadata blocked on dependency retry")
			}
			close(c.release)
			<-done
			<-read
			status, functions = s.Status(), s.Functions()
			if c.initErr != nil {
				if status[0].Enabled || status[0].Error != c.initErr.Error() || len(functions) != 0 || r.backoff != 10*time.Second {
					t.Fatalf("failed retry state = %+v, functions=%+v, backoff=%v", status, functions, r.backoff)
				}
			} else if !status[0].Enabled || status[0].Error != "" || len(functions) != 1 || functions[0].Name != "probe" {
				t.Fatalf("successful retry state = %+v, functions=%+v", status, functions)
			}
			if s.Collector(c.Name()) != c {
				t.Fatal("retry did not publish its collector")
			}
		})
	}
}

func TestSchedulerMetadataDoesNotWaitForDisable(t *testing.T) {
	c := &gatedLifecycleCollector{stage: "stop", started: make(chan struct{}), release: make(chan struct{})}
	r := &running{c: c, desired: true, initialized: true, functions: []Function{{Name: "probe"}},
		status: Status{Name: c.Name(), Enabled: true}}
	s := &Scheduler{cols: []*running{r}}
	done := make(chan struct{})
	go func() { s.SetEnabled(c.Name(), false); close(done) }()
	select {
	case <-c.started:
	case <-time.After(2 * time.Second):
		close(c.release)
		t.Fatal("disable did not reach Stop")
	}
	read := make(chan struct{})
	go func() {
		if s.Status()[0].Enabled || len(s.Functions()) != 0 {
			t.Error("disabled collector still advertised as available")
		}
		close(read)
	}()
	select {
	case <-read:
	case <-time.After(250 * time.Millisecond):
		t.Error("metadata blocked on collector shutdown")
	}
	close(c.release)
	<-done
	<-read
}
