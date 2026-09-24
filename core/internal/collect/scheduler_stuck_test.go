package collect

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// blockedCollector simulates a Collect call wedged in an uninterruptible
// kernel operation (e.g. reading /proc/<pid>/cmdline of a D-state process):
// it never returns and never honors its context.
type blockedCollector struct{ release chan struct{} }

func (c *blockedCollector) Name() string                          { return "blocked" }
func (*blockedCollector) Configure(func(any) error) error         { return nil }
func (*blockedCollector) Init(*registry.Registry) error           { return nil }
func (c *blockedCollector) Collect(context.Context, *registry.Registry, time.Time) error {
	<-c.release // never closed by the test
	return nil
}

// A collector stuck in an uninterruptible syscall must not stall the scheduler
// forever: tick has to return so later rounds keep collecting everything else.
func TestTickReturnsWhenCollectorBlocksForever(t *testing.T) {
	block := &blockedCollector{release: make(chan struct{})}
	r := &running{c: block, desired: true, initialized: true}
	s := &Scheduler{reg: registry.New(&registry.Host{UpdateEvery: 1}, nil), log: slog.Default(),
		cols: []*running{r}, timeout: 300 * time.Millisecond}

	done := make(chan struct{})
	go func() { s.tick(context.Background(), time.Now()); close(done) }()

	select {
	case <-done:
		// pass: the tick returned despite the wedged collector
	case <-time.After(4 * time.Second):
		t.Fatal("tick blocked forever on a collector stuck in an uninterruptible call")
	}
}
