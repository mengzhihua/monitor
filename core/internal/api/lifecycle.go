package api

import (
	"context"
	"time"
)

// startBackground runs only after all fallible initialization in New succeeds.
// The Server owns these workers; dependencies supplied to New remain caller-owned.
func (s *Server) startBackground() {
	ctx, cancel := context.WithCancel(context.Background())
	s.backgroundCancel = cancel
	if s.opt.SelfMonitor {
		s.backgroundWG.Go(func() { s.selfMonitor(ctx) })
	}
	if s.opt.ReportEvery > 0 && s.opt.ReportDir != "" {
		s.backgroundWG.Go(func() { s.exportReports(ctx) })
	}
	if s.opt.Elector != nil {
		s.backgroundWG.Go(func() { s.electLeader(ctx) })
	}
}

func (s *Server) electLeader(ctx context.Context) {
	s.opt.Elector.Tick()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.opt.Elector.Tick()
		}
	}
}

// Close stops and joins background workers, live feeds and WebShell sessions.
// Callers must also shut down their HTTP server before closing shared stores.
// Concurrent and repeated calls all wait until shutdown is complete.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		if s.backgroundCancel != nil {
			s.backgroundCancel()
		}
		if s.live != nil {
			s.live.close()
		}
		if s.shell != nil {
			s.CloseShells()
		}
		s.backgroundWG.Wait()
	})
}
