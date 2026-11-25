package main

import (
	"context"
	"log/slog"
	"time"
)

// watchShutdown bounds the entire shutdown, including deferred cleanup and
// restart. The five-second worker drain is cooperative: a collector blocked in
// native code can ignore cancellation. Only terminating the owning process can
// safely release its database lock in that case. Exit 2 signals an unclean stop
// to the service manager; the next launch uses normal TSDB crash recovery.
func watchShutdown(ctx context.Context, limit time.Duration, log *slog.Logger, exit func(int)) func() {
	finished := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-finished:
			return
		case <-ctx.Done():
		}
		timer := time.NewTimer(limit)
		defer timer.Stop()
		select {
		case <-finished:
			return
		case <-timer.C:
		}
		// Prefer a completed drain if it coincides with the deadline.
		select {
		case <-finished:
			return
		default:
		}
		// A full stderr pipe must not defeat the process deadline either.
		logged := make(chan struct{})
		go func() {
			defer close(logged)
			log.Error("shutdown deadline exceeded; forcing process exit", "timeout", limit, "exit_code", 2,
				"recovery", "next start recovers persisted data; unflushed samples may be lost")
		}()
		logTimer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-logged:
		case <-logTimer.C:
		}
		logTimer.Stop()
		exit(2)
	}()
	return func() {
		close(finished)
		<-done
	}
}
