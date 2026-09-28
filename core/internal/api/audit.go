package api

import (
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/mengzhihua/monitor/core/internal/audit"
	"github.com/mengzhihua/monitor/core/internal/stream"
)

// statusWriter captures the response status for the audit log.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// auditSkip lists high-volume ingest/telemetry paths excluded from the audit
// log. Reads (GET/HEAD/OPTIONS) are never audited either.
func auditSkip(path string) bool {
	switch path {
	case "/api/v1/checks", "/api/v1/ingest/openmetrics", "/api/v1/ingest/otlp",
		"/v1/metrics", stream.Path, stream.PathACLK, "/api/v1/hub/ring", "/api/v1/claim",
		"/api/v1/agent/config":
		return true
	}
	return false
}

// auditWrap records a mutating /api/ request after its handler ran. Called
// after authentication so the caller identity is known.
func (s *Server) auditWrap(u User, r *http.Request, next http.Handler) http.Handler {
	if s.audit == nil || r.Method == http.MethodGet || r.Method == http.MethodHead ||
		r.Method == http.MethodOptions || auditSkip(r.URL.Path) {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, req)
		target := req.URL.Query().Get("alarm_id")
		if target == "" {
			target = req.URL.Query().Get("alarm")
		}
		if target == "" {
			target = req.URL.Query().Get("name")
		}
		if target == "" {
			target = req.URL.Query().Get("node")
		}
		s.audit.Append(audit.Entry{
			TS: time.Now().Unix(), User: u.Name, Role: string(u.Role),
			Remote: remoteHost(req), Method: req.Method, Path: req.URL.Path,
			Action: req.Method + " " + req.URL.Path, Target: target, Status: sw.status,
		})
	})
}

func remoteHost(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// auditEvent records a non-request event (login / login_failed).
func (s *Server) auditEvent(r *http.Request, action, user string) {
	if s.audit == nil {
		return
	}
	s.audit.Append(audit.Entry{TS: time.Now().Unix(), User: user, Remote: remoteHost(r), Path: r.URL.Path, Action: action})
}

// GET /api/v1/audit?after=<unix>&limit=<n≤1000>&user=<name> — admin only.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if u := userOf(r); u.Role != RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if s.audit == nil {
		http.Error(w, "audit log disabled", http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	entries := s.audit.Query(after, limit, q.Get("user"))
	if entries == nil {
		entries = []audit.Entry{}
	}
	writeJSON(w, map[string]any{"entries": entries})
}
