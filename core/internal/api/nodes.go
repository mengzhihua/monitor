package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/hub"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

// Role is what an API credential may do.
type Role string

const (
	// RoleAdmin: everything, including forgetting nodes.
	RoleAdmin Role = "admin"
	// RoleTroubleshooter: read metrics/alarms and run Functions.
	RoleTroubleshooter Role = "troubleshooter"
	// RoleViewer: read-only metrics/alarms/nodes.
	RoleViewer Role = "viewer"
)

// User is a named API credential (`web.users` in monitor.yaml).
type User struct {
	Name  string `json:"name"`
	Token string `json:"-"`
	Role  Role   `json:"role"`
}

type userKey struct{}

var anonymous = User{Name: "anonymous", Role: RoleAdmin}

func validateUsers(us []User) error {
	seen := map[string]bool{}
	for _, u := range us {
		switch u.Role {
		case RoleAdmin, RoleTroubleshooter, RoleViewer:
		default:
			return fmt.Errorf("web.users %q: unknown role %q (admin|troubleshooter|viewer)", u.Name, u.Role)
		}
		if u.Token == "" {
			return fmt.Errorf("web.users %q: empty token", u.Name)
		}
		if seen[u.Token] {
			return fmt.Errorf("web.users %q: token reused", u.Name)
		}
		seen[u.Token] = true
	}
	return nil
}

// authenticate maps the request credential to a user. With no token and no
// users configured the API is open and callers act as admin.
func (s *Server) authenticate(r *http.Request) (User, bool) {
	if s.opt.Token == "" && len(s.opt.Users) == 0 {
		return anonymous, true
	}
	tok := requestToken(r)
	if tok == "" {
		return User{}, false
	}
	if s.opt.Token != "" && tok == s.opt.Token {
		return User{Name: "admin", Role: RoleAdmin}, true
	}
	for _, u := range s.opt.Users {
		if u.Token == tok {
			return u, true
		}
	}
	return User{}, false
}

// allows is the RBAC matrix: reads for everyone, Functions from
// troubleshooter up, mutations admin only.
func (ro Role) allows(r *http.Request) bool {
	switch ro {
	case RoleAdmin:
		return true
	case RoleTroubleshooter:
		return r.Method == http.MethodGet
	default:
		return r.Method == http.MethodGet && r.URL.Path != "/api/v1/function"
	}
}

func userOf(r *http.Request) User {
	if u, ok := r.Context().Value(userKey{}).(User); ok {
		return u
	}
	return anonymous
}

// view is the registry/TSDB pair a request addresses: the local host or, on
// a hub, a streamed node selected with ?node=<id>.
type view struct {
	id       string // "" for local
	hostname string
	reg      *registry.Registry
	db       tsdb.Reader
	node     *hub.Node // nil for local
}

func (s *Server) isLocal(id string) bool {
	return id == "" || id == "local" || id == s.reg.Host.ID
}

func (s *Server) target(w http.ResponseWriter, r *http.Request) (*view, bool) {
	id := r.URL.Query().Get("node")
	// A streamed node wins over the local host when both carry the same ID
	// (agent + hub on one machine share the OS machine-id).
	if s.opt.Nodes != nil && id != "" && id != "local" {
		if n, ok := s.opt.Nodes.Get(id); ok {
			return &view{id: n.ID, hostname: n.Host.Hostname, reg: n.Registry(), db: n.DB(), node: n}, true
		}
	}
	if s.isLocal(id) {
		return &view{hostname: s.reg.Host.Hostname, reg: s.reg, db: s.db}, true
	}
	if s.opt.Nodes == nil {
		http.Error(w, "not a hub: node= is unsupported", http.StatusNotFound)
		return nil, false
	}
	http.Error(w, "unknown node "+id, http.StatusNotFound)
	return nil, false
}

// GET /api/v1/nodes — the local host first, then streamed nodes.
func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	local := hub.Info{ID: "", Hostname: s.reg.Host.Hostname, OS: s.reg.Host.OS, Arch: s.reg.Host.Arch, Labels: s.reg.Host.Labels,
		UpdateEvery: s.reg.Host.UpdateEvery, Version: s.opt.Version, Status: hub.StatusLive, Local: true,
		FirstSeen: s.opt.StartedAt.Unix(), LastSeen: now.Unix(), LastData: now.Unix(), ChartsCount: len(s.reg.Charts()), Alarms: map[string]int{"warning": 0, "critical": 0}}
	if s.opt.Health != nil {
		sum := s.opt.Health.Summary()
		local.Alarms["warning"], local.Alarms["critical"] = sum.Warning, sum.Critical
	}
	for _, f := range s.sched.Functions() {
		local.Functions = append(local.Functions, f.Name)
	}
	out := []hub.Info{local}
	if s.opt.Nodes != nil {
		if st := r.URL.Query().Get("status"); st != "" {
			for _, n := range s.opt.Nodes.List() {
				if inf := n.Info(now); inf.Status == st {
					out = append(out, inf)
				}
			}
		} else {
			for _, n := range s.opt.Nodes.List() {
				out = append(out, n.Info(now))
			}
		}
	}
	writeJSON(w, map[string]any{"now": now.Unix(), "nodes": out})
}

// DELETE /api/v1/nodes?node=<id> — drop an offline node's metadata.
func (s *Server) handleForgetNode(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("node")
	if s.opt.Nodes == nil || id == "" || id == "local" {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	if err := s.opt.Nodes.Forget(id); err != nil {
		code := http.StatusNotFound
		if strings.Contains(err.Error(), "connected") {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	if err := s.opt.Nodes.Save(); err != nil {
		s.log.Error("save nodes", "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}
