package api

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
)

// guard enforces web.allow_from and web.token for /api and /metrics.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.nets) > 0 {
			host, _, _ := net.SplitHostPort(r.RemoteAddr)
			ip := net.ParseIP(host)
			ok := false
			for _, n := range s.nets {
				if ip != nil && n.Contains(ip) {
					ok = true
					break
				}
			}
			if !ok {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		if publicAPI(r.URL.Path) || r.URL.Path == "/api/v1/shell/connect" {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/metrics" || strings.HasPrefix(r.URL.Path, "/v1/") {
			u, ok := s.authenticate(r)
			if !ok {
				s.auditEvent(r, "login_failed", "")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), userKey{}, u))
			// Attribute rejected mutations too, without reading their bodies or
			// invoking the endpoint. Authentication failures keep their separate
			// login_failed event; reads and telemetry remain excluded by auditWrap.
			authorized := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !u.Role.allows(r) {
					http.Error(w, "forbidden: role "+string(u.Role)+" may not "+r.Method+" "+r.URL.Path, http.StatusForbidden)
					return
				}
				if !s.roomAllowed(u, r.URL.Query().Get("node")) {
					http.Error(w, "forbidden: room", http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
			})
			// Local variable: reassigning the closure's `next` would nest
			// wrappers across requests and bake in the first caller's identity.
			h := s.auditWrap(u, r, authorized)
			h.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requestToken extracts the caller's credential from, in order: the
// Authorization header, a `bearer.<token>` WebSocket subprotocol (browsers
// cannot set headers on WebSocket upgrades) or a `token` query parameter.
func requestToken(r *http.Request) string {
	if tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); tok != "" && tok != r.Header.Get("Authorization") {
		return tok
	}
	for _, p := range websocket.Subprotocols(r) {
		if strings.HasPrefix(p, wsTokenProto) {
			// base64url (no padding) keeps any token inside the subprotocol
			// token grammar (RFC 7230 tchar)
			if tok, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(p, wsTokenProto)); err == nil {
				return string(tok)
			}
			return ""
		}
	}
	return r.URL.Query().Get("token")
}
