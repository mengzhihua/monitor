package api

import (
	"net/http"
	"runtime"
	"runtime/pprof"
)

// handleDebugHeap serves a Go heap profile for offline comparison (go tool pprof).
// Restricted to authenticated administrators; intended for soak investigations.
func (s *Server) handleDebugHeap(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	if u.Role != RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	runtime.GC()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="heap.pprof"`)
	w.Header().Set("Cache-Control", "no-store")
	if err := pprof.WriteHeapProfile(w); err != nil {
		http.Error(w, "heap profile failed", http.StatusInternalServerError)
	}
}
