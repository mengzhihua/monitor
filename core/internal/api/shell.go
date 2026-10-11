package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mengzhihua/monitor/core/internal/audit"
	"github.com/mengzhihua/monitor/core/internal/hostshell"
)

const shellTicketTTL = 30 * time.Second
const shellIdle = 5 * time.Minute
const shellMaximum = 30 * time.Minute

type shellTicket struct {
	target  hostshell.Target
	user    User
	token   string
	expires time.Time
}
type shellState struct {
	mu      sync.Mutex
	tickets map[string]shellTicket
	active  map[string]context.CancelFunc
	closed  bool
	wg      sync.WaitGroup
}

func newShellState() *shellState {
	return &shellState{tickets: map[string]shellTicket{}, active: map[string]context.CancelFunc{}}
}

// CloseShells closes hijacked WebSockets/PTYs, which http.Server.Shutdown does not manage.
func (s *Server) CloseShells() {
	s.shell.mu.Lock()
	s.shell.closed = true
	s.shell.tickets = map[string]shellTicket{}
	for _, cancel := range s.shell.active {
		cancel()
	}
	s.shell.mu.Unlock()
	s.shell.wg.Wait()
}
func shellID() string {
	var b [24]byte
	_, e := rand.Read(b[:])
	if e != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}
func (s *Server) shellAccount(w http.ResponseWriter, r *http.Request) (User, bool) {
	w.Header().Set("Cache-Control", "no-store")
	u := userOf(r)
	if u.Role != RoleAdmin || u.principal == "" || r.URL.Query().Has("token") {
		http.Error(w, "shell requires a personal administrator login", 403)
		return u, false
	}
	return u, true
}
func (s *Server) shellNode(t hostshell.Target) string {
	if t.Node == "" || t.Node == "local" {
		return ""
	}
	if t.Node == s.reg.Host.ID {
		if s.opt.Nodes == nil {
			return ""
		}
		if _, exists := s.opt.Nodes.Get(t.Node); !exists {
			return ""
		}
	}
	return t.Node
}
func (s *Server) shellTargetAllowed(u User, t hostshell.Target) bool {
	n := s.shellNode(t)
	if !s.roomAllowed(u, n) {
		return false
	}
	if n != "" {
		if s.opt.Nodes != nil {
			if _, ok := s.opt.Nodes.Get(n); ok {
				return true
			}
		}
		return false
	}
	return true
}
func (s *Server) shellOrigin(r *http.Request) bool {
	v := r.Header.Get("Origin")
	if v == "" {
		return false
	}
	u, e := url.Parse(v)
	if e != nil || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if s.opt.Shell.PublicOrigin != "" {
		return v == s.opt.Shell.PublicOrigin
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return u.Scheme == scheme && u.Host == r.Host
}
func (s *Server) shellSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if s.opt.Shell.PublicOrigin != "" {
		return r.Method == http.MethodGet || r.Header.Get("Origin") == s.opt.Shell.PublicOrigin
	}
	h := (&url.URL{Host: r.Host}).Hostname()
	ip := net.ParseIP(h)
	remote := net.ParseIP(remoteHost(r))
	return (h == "localhost" || ip != nil && ip.IsLoopback()) && remote != nil && remote.IsLoopback()
}
func (s *Server) handleShellTargets(w http.ResponseWriter, r *http.Request) {
	u, ok := s.shellAccount(w, r)
	if !ok {
		return
	}
	targets := []hostshell.Target{}
	if s.opt.Shell.Enabled {
		for _, t := range s.opt.Shell.Targets {
			if s.shellTargetAllowed(u, t) {
				targets = append(targets, t)
			}
		}
	}
	writeJSON(w, map[string]any{"enabled": s.opt.Shell.Enabled, "secure": s.shellSecure(r), "targets": targets, "idle_seconds": int(shellIdle.Seconds()), "max_seconds": int(shellMaximum.Seconds()), "audit_enabled": s.audit != nil})
}
func (s *Server) handleShellTicket(w http.ResponseWriter, r *http.Request) {
	u, ok := s.shellAccount(w, r)
	if !ok {
		return
	}
	if !s.shellOrigin(r) || !s.shellSecure(r) {
		http.Error(w, "shell requires HTTPS and a matching browser origin (loopback HTTP is allowed)", 403)
		return
	}
	if !s.opt.Shell.Enabled {
		http.Error(w, "shell disabled; configure shell.enabled and targets", 503)
		return
	}
	var input struct {
		Target string `json:"target"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid shell target request", 400)
		return
	}
	var t hostshell.Target
	found := false
	for _, v := range s.opt.Shell.Targets {
		if v.ID == input.Target {
			t = v
			found = true
			break
		}
	}
	if !found || !s.shellTargetAllowed(u, t) {
		http.Error(w, "shell target unavailable", 404)
		return
	}
	id := shellID()
	if id == "" {
		http.Error(w, "shell unavailable", 503)
		return
	}
	s.shell.mu.Lock()
	defer s.shell.mu.Unlock()
	for k, t := range s.shell.tickets {
		if time.Now().After(t.expires) {
			delete(s.shell.tickets, k)
		}
	}
	if s.shell.closed {
		http.Error(w, "shell shutting down", 503)
		return
	}
	if s.shell.active[u.principal] != nil || len(s.shell.active) >= 4 {
		http.Error(w, "shell session limit reached", 429)
		return
	}
	for k, t := range s.shell.tickets {
		if t.user.principal == u.principal {
			delete(s.shell.tickets, k)
		}
	}
	if len(s.shell.tickets) >= 64 {
		http.Error(w, "shell ticket limit reached", 429)
		return
	}
	exp := time.Now().Add(shellTicketTTL)
	s.shell.tickets[id] = shellTicket{target: t, user: u, token: requestToken(r), expires: exp}
	withAuditTarget(r, t.ID)
	writeJSON(w, map[string]any{"ticket": id, "expires_at": exp.Unix()})
}

// The ticket has already passed TOTP. Recheck identity/role and session expiry during use.
func (s *Server) shellCurrentUser(t shellTicket) (User, bool) {
	r := &http.Request{Header: http.Header{}, URL: &url.URL{}}
	r.Header.Set("Authorization", "Bearer "+t.token)
	if s.opt.Token != "" && t.user.principal == viewPrincipal("legacy", s.opt.Token) {
		return User{Name: "admin", Role: RoleAdmin, principal: t.user.principal}, true
	}
	for _, u := range s.opt.Users {
		if t.user.principal == viewPrincipal("static", u.Token) {
			u.principal = t.user.principal
			return u, u.Role == RoleAdmin
		}
	}
	u, ok := s.authenticate(r)
	return u, ok && u.principal == t.user.principal && u.Role == RoleAdmin
}
func (s *Server) shellAudit(r *http.Request, t shellTicket, id, action string, status int) {
	s.audit.Append(audit.Entry{TS: time.Now().Unix(), User: t.user.Name, Role: string(t.user.Role), Remote: remoteHost(r), Method: r.Method, Path: "/api/v1/shell/connect", Action: action, Target: t.target.ID + "/" + id, Status: status})
}
func (s *Server) handleShellConnect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.shellOrigin(r) || !s.shellSecure(r) {
		http.Error(w, "shell origin or secure transport required", 403)
		return
	}
	if !s.opt.Shell.Enabled {
		http.Error(w, "shell disabled", 503)
		return
	}
	id := ""
	for _, p := range websocket.Subprotocols(r) {
		if strings.HasPrefix(p, "shell-ticket.") {
			id = strings.TrimPrefix(p, "shell-ticket.")
			break
		}
	}
	if len(id) != 48 || r.URL.RawQuery != "" {
		http.Error(w, "valid shell ticket required", 401)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), shellMaximum)
	defer cancel()
	s.shell.mu.Lock()
	t, ok := s.shell.tickets[id]
	delete(s.shell.tickets, id)
	if !ok || time.Now().After(t.expires) || s.shell.closed {
		s.shell.mu.Unlock()
		http.Error(w, "shell ticket expired or used", 401)
		return
	}
	u, valid := s.shellCurrentUser(t)
	if !valid || !s.shellTargetAllowed(u, t.target) {
		s.shell.mu.Unlock()
		http.Error(w, "shell permission expired", 403)
		return
	}
	if s.shell.active[u.principal] != nil || len(s.shell.active) >= 4 {
		s.shell.mu.Unlock()
		http.Error(w, "shell session limit reached", 429)
		return
	}
	s.shell.active[u.principal] = cancel
	s.shell.wg.Add(1)
	s.shell.mu.Unlock()
	defer func() { s.shell.mu.Lock(); delete(s.shell.active, u.principal); s.shell.mu.Unlock(); s.shell.wg.Done() }()
	upgrader := websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096, Subprotocols: []string{"monitor-shell"}, CheckOrigin: s.shellOrigin}
	ws, e := upgrader.Upgrade(w, r, nil)
	if e != nil {
		return
	}
	defer ws.Close()
	stop := context.AfterFunc(ctx, func() { ws.Close() })
	defer stop()
	send := func(v any) error { _ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second)); return ws.WriteJSON(v) }
	ws.SetReadLimit(96 << 10)
	_ = ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	var first struct {
		Type string `json:"type"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
		hostshell.Credentials
	}
	if ws.ReadJSON(&first) != nil || first.Type != "connect" || len(first.Password) > 4096 || len(first.PrivateKey) > 64<<10 || len(first.Passphrase) > 4096 {
		return
	}
	ses, e := hostshell.Open(ctx, t.target, first.Credentials, first.Cols, first.Rows)
	first.Credentials = hostshell.Credentials{}
	if e != nil {
		_ = send(map[string]any{"type": "error", "message": e.Error()})
		s.shellAudit(r, t, id, "shell_connect_failed", 502)
		return
	}
	defer ses.Close()
	stopSession := context.AfterFunc(ctx, func() { ses.Close() })
	defer stopSession()
	s.shellAudit(r, t, id, "shell_connected", 101)
	defer s.shellAudit(r, t, id, "shell_closed", 200)
	if send(map[string]any{"type": "ready", "target": t.target.ID}) != nil {
		return
	}
	ws.SetReadLimit(20 << 10)
	_ = ws.SetReadDeadline(time.Now().Add(35 * time.Second))
	ws.SetPongHandler(func(string) error { return ws.SetReadDeadline(time.Now().Add(35 * time.Second)) })
	var lastInput atomic.Int64
	lastInput.Store(time.Now().UnixNano())
	go func() {
		defer cancel()
		for {
			var m struct {
				Type string `json:"type"`
				Data string `json:"data"`
				Cols int    `json:"cols"`
				Rows int    `json:"rows"`
			}
			if ws.ReadJSON(&m) != nil {
				return
			}
			switch m.Type {
			case "input":
				if len(m.Data) == 0 || len(m.Data) > 16<<10 {
					return
				}
				lastInput.Store(time.Now().UnixNano())
				if _, e := ses.Write([]byte(m.Data)); e != nil {
					return
				}
			case "resize":
				if m.Cols < 2 || m.Cols > 400 || m.Rows < 2 || m.Rows > 200 {
					return
				}
				if ses.Resize(m.Cols, m.Rows) != nil {
					return
				}
			case "close":
				return
			default:
				return
			}
		}
	}()
	output := make(chan []byte, 8)
	go func() {
		defer close(output)
		buf := make([]byte, 4096)
		var total int
		for {
			n, e := ses.Read(buf)
			if n > 0 {
				total += n
				if total > 32<<20 {
					cancel()
					return
				}
				b := append([]byte(nil), buf[:n]...)
				select {
				case output <- b:
				case <-ctx.Done():
					return
				}
			}
			if e != nil {
				return
			}
		}
	}()
	waited := make(chan error, 1)
	go func() { waited <- ses.Wait() }()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case b, ok := <-output:
			if !ok {
				code := 0
				select {
				case e := <-waited:
					if e != nil {
						code = 1
					}
				case <-time.After(time.Second):
					code = 1
				}
				_ = send(map[string]any{"type": "exit", "code": code})
				return
			}
			_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if ws.WriteMessage(websocket.BinaryMessage, b) != nil {
				return
			}
		case <-ticker.C:
			current, valid := s.shellCurrentUser(t)
			if !valid || !s.shellTargetAllowed(current, t.target) {
				_ = send(map[string]any{"type": "error", "message": "shell permission expired"})
				return
			}
			if time.Since(time.Unix(0, lastInput.Load())) > shellIdle {
				_ = send(map[string]any{"type": "error", "message": "shell idle timeout"})
				return
			}
			_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if ws.WriteMessage(websocket.PingMessage, nil) != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}
