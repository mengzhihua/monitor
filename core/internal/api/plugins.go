package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/mengzhihua/monitor/core/internal/plugins"
)

// PluginRuntime is the platform's process-extension boundary. Concrete process
// supervision stays in plugins; HTTP only authorizes and translates requests.
type PluginRuntime interface {
	Status() []plugins.Status
	Catalog() plugins.Catalog
	Apply(context.Context, plugins.ControlRequest) (plugins.Catalog, error)
}

type platformInfo struct {
	APIVersion      int      `json:"api_version"`
	Scope           string   `json:"scope"`
	Capabilities    []string `json:"capabilities"`
	PluginProtocols []string `json:"plugin_protocols"`
}

func (s *Server) platformInfo(r *http.Request) platformInfo {
	info := platformInfo{APIVersion: 1, Scope: "local", Capabilities: []string{}, PluginProtocols: []string{plugins.ProtocolV1}}
	u := userOf(r)
	if s.opt.Plugins != nil && u.Role == RoleAdmin && u.principal != "" {
		info.Capabilities = append(info.Capabilities, "plugins.catalog.v1", "plugins.control.v1")
	}
	return info
}

func (s *Server) registerPluginRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/platform", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, s.platformInfo(r))
	})
	m.HandleFunc("GET /api/v1/plugins", s.handlePlugins)
	m.HandleFunc("POST /api/v1/plugins/actions", s.handlePluginAction)
}

func (s *Server) allowPlugins(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if node := r.URL.Query().Get("node"); node != "" && node != "local" {
		http.Error(w, "plugin management is local only", http.StatusBadRequest)
		return false
	}
	u := userOf(r)
	if u.Role != RoleAdmin || u.principal == "" {
		http.Error(w, "plugin management requires an authenticated admin", http.StatusForbidden)
		return false
	}
	if s.opt.Plugins == nil {
		http.Error(w, "plugin runtime is disabled", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func (s *Server) handlePlugins(w http.ResponseWriter, r *http.Request) {
	if s.allowPlugins(w, r) {
		writeJSON(w, s.opt.Plugins.Catalog())
	}
}

func (s *Server) handlePluginAction(w http.ResponseWriter, r *http.Request) {
	if !s.allowPlugins(w, r) {
		return
	}
	var request plugins.ControlRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid plugin action", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF || request.IfRevision == 0 {
		http.Error(w, "one action and if_revision are required", http.StatusBadRequest)
		return
	}
	withAuditTarget(r, request.Name)
	withAuditNote(r, request.Action)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	catalog, err := s.opt.Plugins.Apply(ctx, request)
	if err != nil {
		status, message := http.StatusInternalServerError, "plugin operation failed"
		switch {
		case errors.Is(err, plugins.ErrConflict):
			status, message = http.StatusConflict, "plugin catalog changed; refresh before retrying"
		case errors.Is(err, plugins.ErrNotFound):
			status, message = http.StatusNotFound, "plugin not found"
		case errors.Is(err, plugins.ErrPolicy):
			status, message = http.StatusForbidden, "plugin action is blocked by policy or disabled state"
		case errors.Is(err, plugins.ErrInvalid):
			status, message = http.StatusBadRequest, "invalid plugin action"
		case errors.Is(err, plugins.ErrUnavailable):
			status, message = http.StatusServiceUnavailable, "plugin runtime is not running"
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			status, message = http.StatusServiceUnavailable, "plugin operation interrupted; refresh its status"
		}
		http.Error(w, message, status)
		return
	}
	writeJSON(w, catalog)
}
