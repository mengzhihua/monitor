package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/collect"
)

const checkBodyMax = 8 << 10

func (s *Server) handleChecks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if node := r.URL.Query().Get("node"); node != "" && node != "local" {
		http.Error(w, "external checks are local only", http.StatusBadRequest)
		return
	}
	u := userOf(r)
	if r.Method == http.MethodGet {
		if u.Role != RoleAdmin && u.Role != RoleTroubleshooter && u.Role != RoleViewer {
			http.Error(w, "external checks require a signed-in user", http.StatusForbidden)
			return
		}
		now := time.Now()
		writeJSON(w, map[string]any{"now": now.Unix(), "checks": collect.Checks().List(now)})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if u.Role != RoleAdmin && u.Role != RoleTroubleshooter {
		http.Error(w, "external checks require an operator", http.StatusForbidden)
		return
	}
	var body struct {
		Name    string   `json:"name"`
		Status  string   `json:"status"`
		Message string   `json:"message"`
		Value   *float64 `json:"value"`
		TTL     string   `json:"ttl"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, checkBodyMax))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || dec.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid check request", http.StatusBadRequest)
		return
	}
	var ttl time.Duration
	if body.TTL != "" {
		d, err := time.ParseDuration(body.TTL)
		if err != nil {
			http.Error(w, "invalid check ttl", http.StatusBadRequest)
			return
		}
		ttl = d
	}
	now := time.Now()
	if err := collect.Checks().Report(body.Name, body.Status, body.Message, body.Value, ttl, now); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	collect.Checks().Publish(s.reg, now)
	want := strings.TrimSpace(body.Name)
	for _, row := range collect.Checks().List(now) {
		if row.Name == want {
			writeJSON(w, row)
			return
		}
	}
	http.Error(w, "external check was not stored", http.StatusInternalServerError)
}
