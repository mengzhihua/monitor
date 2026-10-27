package collect

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

type countingCollector struct {
	n atomic.Int32
}

func (*countingCollector) Name() string                    { return "counting" }
func (*countingCollector) Configure(func(any) error) error { return nil }
func (*countingCollector) Init(*registry.Registry) error   { return nil }
func (c *countingCollector) Collect(context.Context, *registry.Registry, time.Time) error {
	c.n.Add(1)
	return nil
}

func TestSchedulerSkipsCollectorsWhilePaused(t *testing.T) {
	c := &countingCollector{}
	r := &running{c: c, desired: true, initialized: true, status: Status{Name: c.Name()}}
	s := &Scheduler{
		reg:     registry.New(&registry.Host{UpdateEvery: 1}, nil),
		log:     slog.Default(),
		timeout: time.Second,
		cols:    []*running{r},
	}
	paused := true
	s.SetCollectionPause(func() bool { return paused })
	s.tick(context.Background(), time.Unix(100, 0))
	if c.n.Load() != 0 {
		t.Fatalf("paused tick collected %d", c.n.Load())
	}
	paused = false
	s.tick(context.Background(), time.Unix(101, 0))
	if c.n.Load() != 1 {
		t.Fatalf("resumed tick collected %d", c.n.Load())
	}
}
