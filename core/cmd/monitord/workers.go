package main

import (
	"context"
	"log/slog"
)

// workerGroup gives the composition root one shutdown boundary for all workers.
// Only the owner adds/waits; worker completion is communicated through channels.
type workerGroup struct {
	workers []worker
}

type worker struct {
	name string
	done <-chan struct{}
}

func (g *workerGroup) start(ctx context.Context, name string, run func(context.Context)) {
	done := make(chan struct{})
	g.workers = append(g.workers, worker{name, done})
	go func() {
		defer close(done)
		run(ctx)
	}()
}

// wait shares one deadline across workers; a slow worker cannot extend shutdown
// by another full timeout for each subsequent component.
func (g *workerGroup) wait(ctx context.Context, log *slog.Logger) {
	for _, w := range g.workers {
		select {
		case <-w.done:
			continue
		default:
		}
		select {
		case <-w.done:
		case <-ctx.Done():
			log.Warn("worker did not stop in time", "component", w.name)
		}
	}
}
