package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestWorkersDrainBeforeShutdownReturns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := &workerGroup{}
	finished := make(chan struct{}, 3)
	for range 3 {
		g.start(ctx, "test", func(ctx context.Context) {
			<-ctx.Done()
			finished <- struct{}{}
		})
	}
	cancel()
	deadline, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	var logs bytes.Buffer
	g.wait(deadline, slog.New(slog.NewTextHandler(&logs, nil)))
	if len(finished) != 3 || logs.Len() != 0 {
		t.Fatalf("shutdown did not drain workers: finished=%d logs=%s", len(finished), logs.String())
	}
}

func TestWorkersShareDeadlineAndIdentifyBlockedComponents(t *testing.T) {
	g := &workerGroup{}
	release := make(chan struct{})
	defer func() {
		close(release)
		for _, w := range g.workers {
			<-w.done
		}
	}()
	g.start(context.Background(), "blocked-a", func(context.Context) { <-release })
	g.start(context.Background(), "blocked-b", func(context.Context) { <-release })
	g.start(context.Background(), "finished", func(context.Context) {})
	<-g.workers[2].done
	deadline, cancel := context.WithCancel(context.Background())
	cancel()
	var logs bytes.Buffer
	g.wait(deadline, slog.New(slog.NewTextHandler(&logs, nil)))
	for _, name := range []string{"blocked-a", "blocked-b"} {
		if !strings.Contains(logs.String(), "component="+name) {
			t.Errorf("missing diagnostic for %s: %s", name, logs.String())
		}
	}
	if strings.Contains(logs.String(), "component=finished") {
		t.Fatalf("completed worker reported as blocked: %s", logs.String())
	}
}
