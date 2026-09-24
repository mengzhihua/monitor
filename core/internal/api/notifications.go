package api

import (
	"encoding/json"
	"errors"
	"io"
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
	u := userOf(r)
	writeJSON(w, struct {
		health.NotificationSnapshot
		CanTest bool `json:"can_test"`
	}{s.opt.Health.NotificationDiagnostics(), u.Role == RoleAdmin && u.principal != ""})
}

// Only authenticated administrators may send a fixed message to a configured
// local destination. A request cannot supply credentials, recipients or text.
func (s *Server) handleNotificationTest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	u := userOf(r)
	if u.Role != RoleAdmin || u.principal == "" {
		http.Error(w, "notification tests require an authenticated admin", http.StatusForbidden)
		return
	}
	if node := r.URL.Query().Get("node"); node != "" && node != "local" {
		http.Error(w, "notification tests are local only", http.StatusBadRequest)
		return
	}
	if s.opt.Health == nil {
		http.Error(w, "health engine unavailable", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Channel string `json:"channel"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	d.DisallowUnknownFields()
	if d.Decode(&body) != nil || body.Channel == "" || d.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid notification test request", http.StatusBadRequest)
		return
	}
	err := s.opt.Health.QueueNotificationTest(body.Channel)
	switch {
	case errors.Is(err, health.ErrNotificationChannel):
		http.Error(w, "channel is not configured", http.StatusBadRequest)
	case errors.Is(err, health.ErrNotificationTestLimit):
		w.Header().Set("Retry-After", "30")
		http.Error(w, "wait 30 seconds before another notification test", http.StatusTooManyRequests)
	case err != nil:
		http.Error(w, "notification queue unavailable", http.StatusServiceUnavailable)
	default:
		s.log.Info("notification test queued", "channel", body.Channel, "actor", u.Name)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"queued":true}`))
	}
}
