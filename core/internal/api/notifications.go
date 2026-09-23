package api

import (
	"net/http"

	"github.com/mengzhihua/monitor/core/internal/health"
)

func (s *Server) handleNotificationDiagnostics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// This is the local engine even when the chart workspace selects a Hub node.
	if s.opt.Health == nil {
		writeJSON(w, health.NotificationSnapshot{Scope: "local", Channels: []health.NotificationChannel{}, Recent: []health.NotificationResult{}})
		return
	}
	writeJSON(w, s.opt.Health.NotificationDiagnostics())
}
