package api

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/mengzhihua/monitor/core/internal/hostshell"
)

var (
	errShellRevoked       = errors.New("session_revoked")
	errShellMaximum       = errors.New("max_duration")
	errShellOutputLimit   = errors.New("output_limit")
	errShellShutdown      = errors.New("server_shutdown")
	errShellOutputStalled = errors.New("output_stalled")
	errShellInputOverflow = errors.New("input_overflow")
)

// Public IDs are independent of the single-use connection ticket. Records never
// retain connection credentials or terminal contents.
type shellSession struct {
	id          string
	target      hostshell.Target
	started     time.Time
	lastInput   atomic.Int64
	outputBytes atomic.Int64
	connected   atomic.Bool
	ctx         context.Context
	cancel      context.CancelCauseFunc
	done        chan struct{}
}

type shellSessionView struct {
	ID          string           `json:"id"`
	Target      hostshell.Target `json:"target"`
	StartedAt   int64            `json:"started_at"`
	LastInputAt int64            `json:"last_input_at"`
	OutputBytes int64            `json:"output_bytes"`
	State       string           `json:"state"`
}

func (s *Server) handleShellSessions(w http.ResponseWriter, r *http.Request) {
	u, ok := s.shellAccount(w, r)
	if !ok {
		return
	}
	views := []shellSessionView{}
	s.shell.mu.Lock()
	if v := s.shell.active[u.principal]; v != nil && s.shellTargetAllowed(u, v.target) {
		state := "connecting"
		if v.connected.Load() {
			state = "connected"
		}
		if v.ctx.Err() != nil {
			state = "closing"
		}
		views = append(views, shellSessionView{ID: v.id, Target: v.target, StartedAt: v.started.Unix(), LastInputAt: time.Unix(0, v.lastInput.Load()).Unix(), OutputBytes: v.outputBytes.Load(), State: state})
	}
	s.shell.mu.Unlock()
	writeJSON(w, map[string]any{"sessions": views, "now": time.Now().Unix(), "max_sessions": 1, "global_limit": 4})
}

func (s *Server) handleShellRevoke(w http.ResponseWriter, r *http.Request) {
	u, ok := s.shellAccount(w, r)
	if !ok {
		return
	}
	if !s.shellOrigin(r) || !s.shellSecure(r) {
		http.Error(w, "shell origin or secure transport required", http.StatusForbidden)
		return
	}
	s.shell.mu.Lock()
	v := s.shell.active[u.principal]
	if v == nil || v.id != r.PathValue("id") || !s.shellTargetAllowed(u, v.target) {
		s.shell.mu.Unlock()
		http.Error(w, "shell session unavailable", http.StatusNotFound)
		return
	}
	withAuditTarget(r, v.target.ID+"/"+v.id)
	v.cancel(errShellRevoked)
	s.shell.mu.Unlock()
	// Report success only after cleanup releases the quota. Never remove an
	// active record early: a reconnect must not overlap an old process teardown.
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-v.done:
		w.WriteHeader(http.StatusNoContent)
	case <-timer.C:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, map[string]any{"closing": true})
	case <-r.Context().Done():
	}
}

// shellFlow caps bytes in flight until the browser's terminal parser has
// consumed them. A TCP/WebSocket write alone does not bound xterm's JS queue.
const shellOutputWindow int64 = 64 << 10

type shellFlow struct {
	sent         atomic.Int64
	acked        atomic.Int64
	lastProgress atomic.Int64
	wake         chan struct{}
}

func newShellFlow() *shellFlow {
	f := &shellFlow{wake: make(chan struct{}, 1)}
	f.lastProgress.Store(time.Now().UnixNano())
	return f
}

// Only the WebSocket reader calls acknowledge. Acknowledgements are cumulative
// UTF-8 byte counts, never character counts, and cannot grant future credit.
func (f *shellFlow) acknowledge(n int64) bool {
	previous := f.acked.Load()
	if n < previous || n > f.sent.Load() {
		return false
	}
	if n != previous {
		f.acked.Store(n)
		f.lastProgress.Store(time.Now().UnixNano())
		select {
		case f.wake <- struct{}{}:
		default:
		}
	}
	return true
}

func (f *shellFlow) writable() bool { return f.sent.Load()-f.acked.Load() <= shellOutputWindow-4096 }

func shellCloseReason(ctx context.Context) string {
	switch cause := context.Cause(ctx); cause {
	case errShellRevoked, errShellMaximum, errShellOutputLimit, errShellShutdown, errShellOutputStalled, errShellInputOverflow:
		return cause.Error()
	default:
		return ""
	}
}
