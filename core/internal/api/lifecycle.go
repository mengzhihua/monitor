package api

import "context"

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
}

// Close stops and joins the Server's background workers and WebShell sessions.
// Callers must also shut down their HTTP server before closing shared stores.
// Concurrent and repeated calls all wait until shutdown is complete.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		if s.backgroundCancel != nil {
			s.backgroundCancel()
		}
		if s.shell != nil {
			s.CloseShells()
		}
		s.backgroundWG.Wait()
	})
}
