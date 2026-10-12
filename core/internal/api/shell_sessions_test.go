package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mengzhihua/monitor/core/internal/hub"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

func shellSessionList(t *testing.T, ts *httptest.Server, token string) ([]shellSessionView, string) {
	t.Helper()
	body := shellRequest(t, ts, "/api/v1/shell/sessions", token, "", "", http.StatusOK)
	var list struct {
		Sessions []shellSessionView `json:"sessions"`
		Limit    int                `json:"max_sessions"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	if list.Sessions == nil || list.Limit != 1 {
		t.Fatalf("invalid session inventory: %s", body)
	}
	return list.Sessions, body
}

func shellSessionDelete(t *testing.T, ts *httptest.Server, token, id, origin string, want int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/shell/sessions/"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := (&http.Client{Timeout: 6 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("revoke status %d, want %d: %s", resp.StatusCode, want, body)
	}
}

func shellSessionConnect(t *testing.T, ts *httptest.Server, token string, flow bool) (*websocket.Conn, string, string) {
	t.Helper()
	body := shellRequest(t, ts, "/api/v1/shell/tickets", token, `{"target":"local-test"}`, ts.URL, http.StatusOK)
	var issued struct{ Ticket string }
	if err := json.Unmarshal([]byte(body), &issued); err != nil {
		t.Fatal(err)
	}
	ws, _, err := shellDial(t, ts, issued.Ticket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	if err := ws.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteJSON(map[string]any{"type": "connect", "cols": 80, "rows": 24, "flow_control": flow}); err != nil {
		t.Fatal(err)
	}
	var ready struct {
		Type   string `json:"type"`
		ID     string `json:"session_id"`
		Flow   string `json:"flow_control"`
		Window int64  `json:"window_bytes"`
	}
	if err := ws.ReadJSON(&ready); err != nil {
		t.Fatal(err)
	}
	if ready.Type != "ready" || ready.ID == "" || ready.ID == issued.Ticket {
		t.Fatalf("invalid ready message: %+v", ready)
	}
	if flow && (ready.Flow != "ack" || ready.Window != 64<<10) {
		t.Fatalf("flow control was not negotiated: %+v", ready)
	}
	return ws, ready.ID, issued.Ticket
}

func shellSessionWaitClosed(t *testing.T, ws *websocket.Conn, reason string) {
	t.Helper()
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, _, err := ws.ReadMessage()
		if err == nil {
			continue
		}
		var closed *websocket.CloseError
		if !errors.As(err, &closed) || closed.Code != websocket.CloseNormalClosure || closed.Text != reason {
			t.Fatalf("expected normal close with reason %q, got %v", reason, err)
		}
		return
	}
}

func TestShellSessionInventoryRevokeAndSameNameIsolation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix PTY fixture")
	}
	s, ts := shellTestServer(t)
	s.opt.Token = ""
	s.opt.Users = append(s.opt.Users,
		User{Name: "shared-display-name", Token: "shell-fixture-admin", Role: RoleAdmin},
		User{Name: "shared-display-name", Token: "shell-other-admin", Role: RoleAdmin})
	for _, token := range []string{"shell-viewer", "shell-oncall"} {
		shellRequest(t, ts, "/api/v1/shell/sessions", token, "", "", http.StatusForbidden)
		shellSessionDelete(t, ts, token, "unknown", ts.URL, http.StatusForbidden)
	}
	shellRequest(t, ts, "/api/v1/shell/sessions", "", "", "", http.StatusUnauthorized)
	ws, id, ticket := shellSessionConnect(t, ts, "shell-fixture-admin", false)
	if err := ws.WriteJSON(map[string]any{"type": "input", "data": "printf 'SESSION_%s_MARKER\\n' 'PRIVATE'\n"}); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	for !strings.Contains(output.String(), "SESSION_PRIVATE_MARKER") {
		kind, b, err := ws.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if kind == websocket.BinaryMessage {
			output.Write(b)
		}
	}
	list, body := shellSessionList(t, ts, "shell-fixture-admin")
	if len(list) != 1 || list[0].ID != id || list[0].State != "connected" || list[0].Target.ID != "local-test" || list[0].StartedAt <= 0 || list[0].LastInputAt < list[0].StartedAt || list[0].OutputBytes == 0 {
		t.Fatalf("incorrect live session inventory: %s", body)
	}
	for _, private := range []string{ticket, "shell-fixture-admin", "SESSION_PRIVATE_MARKER", "argv", "password", "private_key", "/bin/sh", s.opt.Shell.Targets[0].Dir} {
		if strings.Contains(body, private) {
			t.Fatalf("session inventory exposed private field %q", private)
		}
	}
	other, _ := shellSessionList(t, ts, "shell-other-admin")
	if len(other) != 0 {
		t.Fatal("same display name exposed another account's session")
	}
	shellSessionDelete(t, ts, "shell-other-admin", id, ts.URL, http.StatusNotFound)
	shellSessionDelete(t, ts, "shell-fixture-admin", id, "", http.StatusForbidden)
	shellSessionDelete(t, ts, "shell-fixture-admin", id, "https://elsewhere.example", http.StatusForbidden)
	shellSessionDelete(t, ts, "shell-fixture-admin", ticket, ts.URL, http.StatusNotFound)
	otherWS, otherID, _ := shellSessionConnect(t, ts, "shell-other-admin", false)
	other, _ = shellSessionList(t, ts, "shell-other-admin")
	if len(other) != 1 || other[0].ID != otherID || otherID == id {
		t.Fatal("distinct accounts did not receive isolated session quotas")
	}
	shellSessionDelete(t, ts, "shell-fixture-admin", id, ts.URL, http.StatusNoContent)
	shellSessionWaitClosed(t, ws, "session_revoked")
	list, _ = shellSessionList(t, ts, "shell-fixture-admin")
	if len(list) != 0 {
		t.Fatal("successful revocation left an active session")
	}
	// A 204 must mean cleanup is complete, so a new connection is immediately usable.
	replacement, replacementID, _ := shellSessionConnect(t, ts, "shell-fixture-admin", false)
	if replacementID == id {
		t.Fatal("replacement reused a public session ID")
	}
	other, _ = shellSessionList(t, ts, "shell-other-admin")
	if len(other) != 1 || other[0].ID != otherID {
		t.Fatal("revocation affected another account")
	}
	shellSessionDelete(t, ts, "shell-fixture-admin", replacementID, ts.URL, http.StatusNoContent)
	shellSessionWaitClosed(t, replacement, "session_revoked")
	shellSessionDelete(t, ts, "shell-other-admin", otherID, ts.URL, http.StatusNoContent)
	shellSessionWaitClosed(t, otherWS, "session_revoked")
}

func TestShellConnectingSessionRevokeRechecksRoom(t *testing.T) {
	s, ts := shellTestServer(t)
	s.opt.Token = ""
	s.opt.Users = append(s.opt.Users, User{Name: "room-admin", Token: "shell-fixture-admin", Role: RoleAdmin, Rooms: []string{"allowed"}})
	nodes, err := hub.Open(s.db, t.TempDir(), hub.Options{})
	if err != nil {
		t.Fatal(err)
	}
	node, err := nodes.AcceptReplica(registry.Host{ID: "shell-room-node", Hostname: "remote", UpdateEvery: 1}, nil, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	node.SetMembership("space", "allowed")
	s.opt.Nodes = nodes
	s.opt.Shell.Targets[0].Kind = "ssh"
	s.opt.Shell.Targets[0].Node = node.ID
	ws, _, err := shellDial(t, ts, shellGetTicket(t, ts))
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	// No initialization frame: cancellation must interrupt the initial WS read too.
	list, _ := shellSessionList(t, ts, "shell-fixture-admin")
	if len(list) != 1 || list[0].State != "connecting" {
		t.Fatal("connecting session missing from inventory")
	}
	id := list[0].ID
	node.SetMembership("space", "forbidden")
	list, _ = shellSessionList(t, ts, "shell-fixture-admin")
	if len(list) != 0 {
		t.Fatal("session in a revoked room was exposed")
	}
	shellSessionDelete(t, ts, "shell-fixture-admin", id, ts.URL, http.StatusNotFound)
	node.SetMembership("space", "allowed")
	shellSessionDelete(t, ts, "shell-fixture-admin", id, ts.URL, http.StatusNoContent)
	shellSessionWaitClosed(t, ws, "session_revoked")
	_ = shellGetTicket(t, ts)
}

func TestShellConnectingSSHHandshakeCanBeRevoked(t *testing.T) {
	s, ts := shellTestServer(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	target := &s.opt.Shell.Targets[0]
	target.Kind, target.Address, target.User = "ssh", listener.Addr().String(), "fixture"
	target.HostKey = "SHA256:" + strings.Repeat("A", 43)
	target.Argv, target.Dir = nil, ""
	type acceptedConnection struct {
		conn net.Conn
		err  error
	}
	accepted := make(chan acceptedConnection, 1)
	go func() { conn, err := listener.Accept(); accepted <- acceptedConnection{conn, err} }()
	ws, _, err := shellDial(t, ts, shellGetTicket(t, ts))
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	if err := ws.WriteJSON(map[string]any{"type": "connect", "cols": 80, "rows": 24, "password": "shell-connecting-fixture-secret"}); err != nil {
		t.Fatal(err)
	}
	var remote net.Conn
	select {
	case connected := <-accepted:
		if connected.err != nil {
			t.Fatal(connected.err)
		}
		remote = connected.conn
	case <-time.After(5 * time.Second):
		t.Fatal("SSH client did not connect to the local fixture")
	}
	defer remote.Close()
	remote.SetReadDeadline(time.Now().Add(8 * time.Second))
	disconnected := make(chan error, 1)
	// Drain the client banner but never send a server banner. Open is blocked
	// inside the SSH handshake rather than waiting for the first WS frame.
	go func() { _, err := io.Copy(io.Discard, remote); disconnected <- err }()
	list, body := shellSessionList(t, ts, "shell-fixture-admin")
	if len(list) != 1 || list[0].State != "connecting" {
		t.Fatal("SSH handshake session was not listed as connecting")
	}
	if strings.Contains(body, "shell-connecting-fixture-secret") || strings.Contains(body, "password") {
		t.Fatal("SSH credential exposed in session inventory")
	}
	shellSessionDelete(t, ts, "shell-fixture-admin", list[0].ID, ts.URL, http.StatusNoContent)
	shellSessionWaitClosed(t, ws, "session_revoked")
	select {
	case err := <-disconnected:
		if err != nil {
			t.Fatalf("SSH socket failed to close cleanly: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("revocation left the SSH transport open")
	}
	_ = shellGetTicket(t, ts)
}

type shellReadEvent struct {
	kind int
	data []byte
	err  error
}

func shellSessionReader(t *testing.T, ws *websocket.Conn) <-chan shellReadEvent {
	t.Helper()
	stop := make(chan struct{})
	events := make(chan shellReadEvent, 16)
	t.Cleanup(func() { close(stop); ws.Close() })
	go func() {
		defer close(events)
		for {
			kind, data, err := ws.ReadMessage()
			select {
			case events <- shellReadEvent{kind, data, err}:
			case <-stop:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return events
}

func TestShellOutputAcknowledgementsBoundAndResumeRealPTY(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix PTY fixture")
	}
	s, ts := shellTestServer(t)
	s.opt.Shell.Targets[0].Argv = []string{"/bin/sh", "-c", "stty -echo; dd if=/dev/zero bs=4096 count=48 2>/dev/null; printf 'FLOW_%s_END' 'WINDOW'"}
	ws, id, _ := shellSessionConnect(t, ts, "shell-fixture-admin", true)
	events := shellSessionReader(t, ws)
	const window int64 = 64 << 10
	var received, acknowledged int64
	var output bytes.Buffer
	readOutput := func() shellReadEvent {
		t.Helper()
		select {
		case event := <-events:
			if event.err != nil {
				t.Fatal(event.err)
			}
			return event
		case <-time.After(5 * time.Second):
			t.Fatal("terminal output stalled after acknowledgement")
		}
		return shellReadEvent{}
	}
	for received <= window-4096 {
		event := readOutput()
		if event.kind != websocket.BinaryMessage {
			t.Fatalf("unexpected control frame before window filled: %s", event.data)
		}
		received += int64(len(event.data))
		output.Write(event.data)
	}
	if received > window {
		t.Fatalf("unacknowledged output exceeded window: %d", received)
	}
	// Do not use a WS read deadline here: Gorilla marks timed-out readers unusable.
	select {
	case event := <-events:
		t.Fatalf("output continued without parser acknowledgement: %+v", event)
	case <-time.After(250 * time.Millisecond):
	}
	list, _ := shellSessionList(t, ts, "shell-fixture-admin")
	if len(list) != 1 || list[0].ID != id || list[0].OutputBytes != received {
		t.Fatal("output accounting includes unsent buffered bytes")
	}
	acknowledged = received
	if err := ws.WriteJSON(map[string]any{"type": "ack", "bytes": acknowledged}); err != nil {
		t.Fatal(err)
	}
	for {
		event := readOutput()
		if event.kind == websocket.BinaryMessage {
			received += int64(len(event.data))
			output.Write(event.data)
			if received-acknowledged > window {
				t.Fatal("resumed output exceeded credit")
			}
			if received-acknowledged > window-4096 {
				acknowledged = received
				if err := ws.WriteJSON(map[string]any{"type": "ack", "bytes": acknowledged}); err != nil {
					t.Fatal(err)
				}
			}
			continue
		}
		var eventType struct {
			Type string `json:"type"`
			Code int    `json:"code"`
		}
		if err := json.Unmarshal(event.data, &eventType); err != nil || eventType.Type != "exit" || eventType.Code != 0 {
			t.Fatalf("terminal did not exit cleanly: %s", event.data)
		}
		break
	}
	if received != 48*4096+int64(len("FLOW_WINDOW_END")) || !bytes.HasSuffix(output.Bytes(), []byte("FLOW_WINDOW_END")) {
		t.Fatalf("PTY output was lost or duplicated: %d bytes", received)
	}
}

func TestShellRejectsInvalidOutputAcknowledgements(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix PTY fixture")
	}
	for _, tc := range []struct {
		name     string
		flow     bool
		ack      any
		firstAck bool
	}{
		{"negative", true, int64(-1), false},
		{"future", true, int64(1 << 30), false},
		{"fractional", true, 0.5, false},
		{"regressive", true, int64(3), true},
		{"not-negotiated", false, int64(0), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ts := shellTestServer(t)
			s.opt.Shell.Targets[0].Argv = []string{"/bin/sh", "-c", "printf FLOW; sleep 30"}
			ws, _, _ := shellSessionConnect(t, ts, "shell-fixture-admin", tc.flow)
			var received int64
			for received < 4 {
				kind, data, err := ws.ReadMessage()
				if err != nil {
					t.Fatal(err)
				}
				if kind != websocket.BinaryMessage {
					t.Fatalf("unexpected terminal control: %s", data)
				}
				received += int64(len(data))
			}
			if tc.firstAck {
				if err := ws.WriteJSON(map[string]any{"type": "ack", "bytes": received}); err != nil {
					t.Fatal(err)
				}
			}
			if err := ws.WriteJSON(map[string]any{"type": "ack", "bytes": tc.ack}); err != nil {
				t.Fatal(err)
			}
			shellSessionWaitClosed(t, ws, "")
			deadline := time.Now().Add(3 * time.Second)
			for {
				list, _ := shellSessionList(t, ts, "shell-fixture-admin")
				if len(list) == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("invalid ACK did not release the session quota")
				}
				time.Sleep(10 * time.Millisecond)
			}
			_ = shellGetTicket(t, ts)
		})
	}
}

func TestShellControlCloseWhilePTYInputIsBlocked(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix PTY fixture")
	}
	s, ts := shellTestServer(t)
	// Raw, non-echoing input is never read while sleep owns the foreground PTY.
	// Filling its small kernel buffer must not prevent the WS reader seeing close.
	s.opt.Shell.Targets[0].Argv = []string{"/bin/sh", "-c", "stty -icanon -echo; printf INPUT_READY; sleep 30"}
	ws, id, _ := shellSessionConnect(t, ts, "shell-fixture-admin", true)
	var output strings.Builder
	for !strings.Contains(output.String(), "INPUT_READY") {
		kind, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if kind == websocket.BinaryMessage {
			output.Write(data)
		}
	}
	for i := 0; i < 5; i++ {
		if err := ws.WriteJSON(map[string]any{"type": "input", "data": strings.Repeat("x", 16<<10)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ws.WriteJSON(map[string]any{"type": "close"}); err != nil {
		t.Fatal(err)
	}
	shellSessionWaitClosed(t, ws, "")
	deadline := time.Now().Add(3 * time.Second)
	for {
		list, _ := shellSessionList(t, ts, "shell-fixture-admin")
		if len(list) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("blocked input kept session %s alive", id)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = shellGetTicket(t, ts)
}

func TestShellEscapedInputFitsDecodedByteLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix PTY fixture")
	}
	s, ts := shellTestServer(t)
	s.opt.Shell.Targets[0].Argv = []string{"/bin/sh", "-c", "stty -icanon -echo; printf READY; sleep 30"}
	ws, _, _ := shellSessionConnect(t, ts, "shell-fixture-admin", false)
	var output strings.Builder
	for !strings.Contains(output.String(), "READY") {
		kind, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if kind == websocket.BinaryMessage {
			output.Write(data)
		}
	}
	// NUL expands to six bytes in JSON. This remains a legal 16 KiB input,
	// and the following close must be processed, not rejected as a 1009 frame.
	if err := ws.WriteJSON(map[string]any{"type": "input", "data": strings.Repeat("\x00", 16<<10)}); err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteJSON(map[string]any{"type": "close"}); err != nil {
		t.Fatal(err)
	}
	shellSessionWaitClosed(t, ws, "")
}
