package collect

import "context"

func (s *Scheduler) lifecycle() context.Context {
	s.lifecycleOnce.Do(func() {
		s.stopCtx, s.cancel = context.WithCancel(context.Background())
	})
	return s.stopCtx
}

// Close permanently stops scheduling and releases all collector instances,
// including those initialized before Run starts. It cancels Run and waits for
// in-flight Init and Collect calls before stopping their collector. Like the
// collector interface itself, shutdown relies on Collect honoring its context.
// Concurrent and repeated calls wait for the same cleanup.
func (s *Scheduler) Close() {
	s.lifecycle()
	s.closeOnce.Do(func() {
		s.cancel()
		// No new initWG.Add or collector operation can start after this barrier.
		s.scheduleMu.Lock()
		s.scheduleMu.Unlock()
		s.initWG.Wait()
		for _, r := range s.cols {
			r.opMu.Lock()
			r.mu.Lock()
			r.desired, r.initialized, r.status.Enabled = false, false, false
			r.functions = nil
			r.mu.Unlock()
			r.stop()
			r.opMu.Unlock()
		}
	})
}

// stop requires opMu once the instance has been published by NewScheduler.
func (r *running) stop() {
	if r.stopped {
		return
	}
	r.stopped = true
	if st, ok := r.c.(Stopper); ok {
		st.Stop()
	}
}
