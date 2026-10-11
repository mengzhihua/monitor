package api

import (
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/mengzhihua/monitor/core/internal/audit"
	"github.com/mengzhihua/monitor/core/internal/hostshell"
	"github.com/mengzhihua/monitor/core/internal/hub"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

func shellTestServer(t *testing.T) (*Server, *httptest.Server) {
	log, e := audit.Open("", 100)
	if e != nil {
		t.Fatal(e)
	}
	s, _ := aiTestServer(t, Options{Token: "shell-fixture-admin", Users: []User{{Name: "viewer", Token: "shell-viewer", Role: RoleViewer}, {Name: "oncall", Token: "shell-oncall", Role: RoleTroubleshooter}}, Audit: log, Shell: hostshell.Config{Enabled: true, Targets: []hostshell.Target{{ID: "local-test", Name: "Local", Kind: "local", Argv: []string{"/bin/sh"}, Dir: t.TempDir()}}}})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { s.CloseShells(); ts.Close(); log.Close() })
	return s, ts
}
func shellRequest(t *testing.T, ts *httptest.Server, path, token, body, origin string, want int) string {
	t.Helper()
	method := "GET"
	if body != "" {
		method = "POST"
	}
	r, e := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.Header.Set("Content-Type", "application/json")
	resp, e := http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s status %d want %d", path, resp.StatusCode, want)
	}
	return string(b)
}
func shellGetTicket(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	b := shellRequest(t, ts, "/api/v1/shell/tickets", "shell-fixture-admin", `{"target":"local-test"}`, ts.URL, 200)
	var v struct{ Ticket string }
	if json.Unmarshal([]byte(b), &v) != nil || len(v.Ticket) != 48 {
		t.Fatal("invalid ticket")
	}
	return v.Ticket
}
func shellDial(t *testing.T, ts *httptest.Server, ticket string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	d := websocket.Dialer{Subprotocols: []string{"monitor-shell", "shell-ticket." + ticket}}
	return d.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/api/v1/shell/connect", http.Header{"Origin": {ts.URL}})
}
func TestShellAccessOriginTicketsAndDisabled(t *testing.T) {
	s, ts := shellTestServer(t)
	shellRequest(t, ts, "/api/v1/shell/targets", "", "", "", 401)
	for _, tok := range []string{"shell-viewer", "shell-oncall"} {
		shellRequest(t, ts, "/api/v1/shell/targets", tok, "", "", 403)
		shellRequest(t, ts, "/api/v1/shell/tickets", tok, `{"target":"local-test"}`, ts.URL, 403)
	}
	list := shellRequest(t, ts, "/api/v1/shell/targets", "shell-fixture-admin", "", "", 200)
	if strings.Contains(list, "argv") || strings.Contains(list, "/bin/sh") {
		t.Fatal("private process configuration exposed")
	}
	shellRequest(t, ts, "/api/v1/shell/tickets", "shell-fixture-admin", `{"target":"local-test"}`, "", 403)
	shellRequest(t, ts, "/api/v1/shell/tickets", "shell-fixture-admin", `{"target":"local-test"}`, "http://evil.example", 403)
	shellRequest(t, ts, "/api/v1/shell/tickets", "shell-fixture-admin", `{"target":"local-test","address":"evil"}`, ts.URL, 400)
	shellRequest(t, ts, "/api/v1/shell/tickets", "shell-fixture-admin", `{"target":"unknown"}`, ts.URL, 404)
	first := shellGetTicket(t, ts)
	second := shellGetTicket(t, ts)
	if ws, resp, e := shellDial(t, ts, first); e == nil {
		ws.Close()
		t.Fatal("superseded ticket accepted")
	} else if resp.StatusCode != 401 {
		t.Fatal(resp.StatusCode)
	}
	s.shell.mu.Lock()
	v := s.shell.tickets[second]
	v.expires = time.Now().Add(-time.Second)
	s.shell.tickets[second] = v
	s.shell.mu.Unlock()
	if ws, resp, e := shellDial(t, ts, second); e == nil {
		ws.Close()
		t.Fatal("expired ticket accepted")
	} else if resp.StatusCode != 401 {
		t.Fatal(resp.StatusCode)
	}
	s.opt.Shell.Enabled = false
	shellRequest(t, ts, "/api/v1/shell/tickets", "shell-fixture-admin", `{"target":"local-test"}`, ts.URL, 503)
}
func TestShellWebSocketPTYResizeIsolationReplayAndAudit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix fixture; Windows ConPTY covered by platform build")
	}
	s, ts := shellTestServer(t)
	id := shellGetTicket(t, ts)
	ws, _, e := shellDial(t, ts, id)
	if e != nil {
		t.Fatal(e)
	}
	defer ws.Close()
	ws.SetReadDeadline(time.Now().Add(8 * time.Second))
	if e = ws.WriteJSON(map[string]any{"type": "connect", "cols": 80, "rows": 24}); e != nil {
		t.Fatal(e)
	}
	var ready map[string]any
	if ws.ReadJSON(&ready) != nil || ready["type"] != "ready" {
		t.Fatal("terminal not ready")
	}
	if old, resp, e := shellDial(t, ts, id); e == nil {
		old.Close()
		t.Fatal("replayed ticket accepted")
	} else if resp.StatusCode != 401 {
		t.Fatal(resp.StatusCode)
	}
	shellRequest(t, ts, "/api/v1/shell/tickets", "shell-fixture-admin", `{"target":"local-test"}`, ts.URL, 429)
	ws.WriteJSON(map[string]any{"type": "resize", "cols": 99, "rows": 37})
	ws.WriteJSON(map[string]any{"type": "input", "data": "stty size; printf 'API_%s_READY\\n' '__PTY__'; exit\n"})
	var output strings.Builder
	exit := false
	for {
		kind, b, e := ws.ReadMessage()
		if e != nil {
			break
		}
		if kind == websocket.BinaryMessage {
			output.Write(b)
		} else {
			var event map[string]any
			json.Unmarshal(b, &event)
			if event["type"] == "exit" {
				exit = true
				break
			}
		}
	}
	if !exit || !strings.Contains(output.String(), "37 99") || !strings.Contains(output.String(), "API___PTY___READY") {
		t.Fatal("PTY output, resize or clean exit failed")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.shell.mu.Lock()
		n := len(s.shell.active)
		s.shell.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("session quota not released")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = shellGetTicket(t, ts)
	entries := s.audit.Query(0, 100, "")
	var started, closed bool
	for _, e := range entries {
		if e.Action == "shell_connected" {
			started = true
		}
		if e.Action == "shell_closed" {
			closed = true
		}
		if strings.Contains(fmt.Sprint(e), "stty size") || strings.Contains(fmt.Sprint(e), "shell-fixture-admin") {
			t.Fatal("input or credential was audited")
		}
	}
	if !started || !closed {
		t.Fatal("session audit incomplete")
	}
}
func TestShellRejectsAnonymousAndCleartextRemote(t *testing.T) {
	s, _ := aiTestServer(t, Options{Shell: hostshell.Config{Enabled: true}})
	req := httptest.NewRequest("GET", "http://127.0.0.1:1234/api/v1/shell/targets", nil)
	req.RemoteAddr = "127.0.0.1:4567"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("anonymous administrator accepted")
	}
	s.opt.Token = "fixture"
	req = httptest.NewRequest("POST", "http://monitor.example/api/v1/shell/tickets", strings.NewReader(`{"target":"local-test"}`))
	req.Header.Set("Authorization", "Bearer fixture")
	req.Header.Set("Origin", "http://monitor.example")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.RemoteAddr = "127.0.0.1:4567"
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("untrusted forwarded TLS accepted")
	}
}
func TestShellShutdownClosesHijackedConnection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix fixture")
	}
	s, ts := shellTestServer(t)
	ws, _, e := shellDial(t, ts, shellGetTicket(t, ts))
	if e != nil {
		t.Fatal(e)
	}
	defer ws.Close()
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	ws.WriteJSON(map[string]any{"type": "connect", "cols": 80, "rows": 24})
	var event map[string]any
	if ws.ReadJSON(&event) != nil {
		t.Fatal("not connected")
	}
	s.CloseShells()
	for {
		if _, _, e := ws.ReadMessage(); e != nil {
			break
		}
	}
	shellRequest(t, ts, "/api/v1/shell/tickets", "shell-fixture-admin", `{"target":"local-test"}`, ts.URL, 503)
}

func TestShellRoomRevocationAndIdentityCollision(t *testing.T) {
	s, ts := shellTestServer(t)
	s.opt.Token = ""
	s.opt.Users = append(s.opt.Users, User{Name: "restricted-admin", Token: "shell-fixture-admin", Role: RoleAdmin, Rooms: []string{"allowed"}})
	nodes, err := hub.Open(s.db, t.TempDir(), hub.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// A streamed node sharing the local host ID must still obey its own room.
	node, err := nodes.AcceptReplica(registry.Host{ID: s.reg.Host.ID, Hostname: "remote", UpdateEvery: 1}, nil, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.opt.Nodes = nodes
	node.SetMembership("space", "forbidden")
	s.opt.Shell.Targets[0].Kind = "ssh"
	s.opt.Shell.Targets[0].Node = node.ID
	list := shellRequest(t, ts, "/api/v1/shell/targets", "shell-fixture-admin", "", "", 200)
	if strings.Contains(list, "local-test") {
		t.Fatal("target in forbidden room exposed")
	}
	shellRequest(t, ts, "/api/v1/shell/tickets", "shell-fixture-admin", `{"target":"local-test"}`, ts.URL, 404)
	node.SetMembership("space", "allowed")
	ticket := shellGetTicket(t, ts)
	node.SetMembership("space", "forbidden")
	if ws, resp, err := shellDial(t, ts, ticket); err == nil {
		ws.Close()
		t.Fatal("revoked room accepted")
	} else if resp.StatusCode != 403 {
		t.Fatal(resp.StatusCode)
	}
	share := s.shares.issue("", time.Hour).Token
	shellRequest(t, ts, "/api/v1/shell/targets", share, "", "", 403)
}
func TestShellTicketPreservesValidatedSecondFactorAndRejectsCredentialRotation(t *testing.T) {
	s, ts := shellTestServer(t)
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	s.opt.TOTPSecret = secret
	shellRequest(t, ts, "/api/v1/shell/tickets", "shell-fixture-admin", `{"target":"local-test"}`, ts.URL, 401)
	r, _ := http.NewRequest("POST", ts.URL+"/api/v1/shell/tickets", strings.NewReader(`{"target":"local-test"}`))
	r.Header.Set("Authorization", "Bearer shell-fixture-admin")
	r.Header.Set("Origin", ts.URL)
	r.Header.Set("X-Monitor-OTP", hotp(decodeSecret(t, secret), uint64(time.Now().Unix()/30)))
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	var got struct{ Ticket string }
	json.NewDecoder(resp.Body).Decode(&got)
	s.shell.mu.Lock()
	ticket := s.shell.tickets[got.Ticket]
	s.shell.mu.Unlock()
	if _, valid := s.shellCurrentUser(ticket); !valid {
		t.Fatal("validated second factor lost")
	}
	s.opt.Token = "rotated-fixture-token"
	if ws, resp, err := shellDial(t, ts, got.Ticket); err == nil {
		ws.Close()
		t.Fatal("rotated credential accepted")
	} else if resp.StatusCode != 403 {
		t.Fatal(resp.StatusCode)
	}
}

func TestShellLoopbackAliasesAndProxyOrigin(t *testing.T) {
	s, _ := shellTestServer(t)
	for _, host := range []string{"localhost:19999", "127.0.0.1:19999", "[::1]:19999", "[::1]"} {
		r := httptest.NewRequest("GET", "http://"+host+"/api/v1/shell/targets", nil)
		r.RemoteAddr = "127.0.0.1:1234"
		if !s.shellSecure(r) {
			t.Fatal("loopback alias rejected", host)
		}
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:19999/api/v1/shell/tickets", nil)
	r.RemoteAddr = "192.168.1.10:1234"
	r.Header.Set("Origin", "http://127.0.0.1:19999")
	if s.shellSecure(r) {
		t.Fatal("remote HTTP accepted")
	}
	s.opt.Shell.PublicOrigin = "https://monitor.example.com"
	r.Header.Set("Origin", s.opt.Shell.PublicOrigin)
	if !s.shellSecure(r) || !s.shellOrigin(r) {
		t.Fatal("configured HTTPS proxy rejected")
	}
	r.Header.Set("Origin", "https://evil.example.com")
	if s.shellOrigin(r) {
		t.Fatal("cross origin proxy accepted")
	}
}
