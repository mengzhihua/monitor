package main

import (
	"net/http"
	"sync"
)

// loadingGate accepts connections before the TSDB and API are ready.
// /healthz stays non-200 until use() so existing wait loops keep polling.
type loadingGate struct {
	mu sync.RWMutex
	h  http.Handler
}

func (g *loadingGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.RLock()
	h := g.h
	g.mu.RUnlock()
	if h != nil {
		h.ServeHTTP(w, r)
		return
	}
	if r.URL.Path == "/healthz" {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("loading\n"))
		return
	}
	// Acceptance scripts treat any HTTP status as a final answer and only
	// retry when the dial fails. Until the database is open, drop other
	// connections the way a closed port did.
	if hj, ok := w.(http.Hijacker); ok {
		conn, _, err := hj.Hijack()
		if err == nil {
			_ = conn.Close()
			return
		}
	}
	http.Error(w, "loading", http.StatusServiceUnavailable)
}

func (g *loadingGate) use(h http.Handler) {
	g.mu.Lock()
	g.h = h
	g.mu.Unlock()
}
