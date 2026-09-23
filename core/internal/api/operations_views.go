package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/mengzhihua/monitor/core/internal/operations"
)

// The private key is never returned by the API. Display names are not identities:
// static credentials, verified OIDC subjects and LDAP logins have separate namespaces.
func viewPrincipal(parts ...string) string {
	b, _ := json.Marshal(parts)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type viewsResponse struct {
	operations.ViewCollection
	Enabled    bool `json:"enabled"`
	Persistent bool `json:"persistent"`
	Limit      int  `json:"limit"`
	User       User `json:"user"`
}

func (s *Server) handleOperationsViews(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	u := userOf(r)
	enabled := u.principal != ""
	respond := func(c operations.ViewCollection) {
		writeJSON(w, viewsResponse{ViewCollection: c, Enabled: enabled, Persistent: s.views.Persistent(), Limit: operations.ViewLimit, User: User{Name: u.Name, Role: u.Role}})
	}
	if r.Method == http.MethodGet {
		respond(s.views.Get(u.principal))
		return
	}
	if !enabled {
		http.Error(w, "personal views require an account; anonymous and share links are unsupported", http.StatusForbidden)
		return
	}
	var body struct {
		Revision *uint64                `json:"revision"`
		Views    []operations.SavedView `json:"views"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil || body.Revision == nil {
		http.Error(w, "invalid saved views request", http.StatusBadRequest)
		return
	}
	if d.Decode(new(any)) != io.EOF {
		http.Error(w, "expected one JSON object", http.StatusBadRequest)
		return
	}
	c, err := s.views.Replace(u.principal, *body.Revision, body.Views)
	switch {
	case errors.Is(err, operations.ErrConflict):
		http.Error(w, "views changed; reload and review before retrying", http.StatusConflict)
	case errors.Is(err, operations.ErrInvalidView):
		http.Error(w, "invalid views: at most 10 unique names (1-40 characters), query at most 1024 UTF-8 bytes and valid filters required", http.StatusBadRequest)
	case errors.Is(err, operations.ErrViewCapacity):
		http.Error(w, "saved views storage capacity reached", http.StatusUnprocessableEntity)
	case err != nil:
		s.log.Error("save personal views failed", "error", err)
		http.Error(w, "saved views storage unavailable", http.StatusServiceUnavailable)
	default:
		respond(c)
	}
}
