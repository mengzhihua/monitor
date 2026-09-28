// Package audit keeps a bounded, append-only log of mutating API calls and
// auth events (audit-log.jsonl). It answers "who did what, when".
package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Entry is one audited request or auth event.
type Entry struct {
	TS     int64  `json:"ts"`
	User   string `json:"user,omitempty"`
	Role   string `json:"role,omitempty"`
	Remote string `json:"remote,omitempty"`
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
	Action string `json:"action"` // e.g. "POST /api/v1/alarms/close", "login", "login_failed"
	Target string `json:"target,omitempty"`
	Status int    `json:"status,omitempty"`
}

// Log is an in-memory ring over an append-only JSONL file. When the memory
// count passes 2*max the file is rewritten keeping the newest max entries.
type Log struct {
	mu       sync.Mutex
	path     string
	max      int
	file     *os.File
	entries  []Entry // oldest → newest
	maxLimit int
}

const fileName = "audit-log.jsonl"

// Open loads dir/audit-log.jsonl (missing file = empty log). dir="" keeps
// the log in memory only. max<=0 means 10000 entries.
func Open(dir string, max int) (*Log, error) {
	if max <= 0 {
		max = 10000
	}
	l := &Log{max: max, maxLimit: 1000}
	if dir == "" {
		return l, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	l.path = filepath.Join(dir, fileName)
	if f, err := os.Open(l.path); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			var e Entry
			if json.Unmarshal(sc.Bytes(), &e) == nil && e.Action != "" {
				l.entries = append(l.entries, e)
			}
		}
		f.Close()
		if len(l.entries) > max {
			l.entries = l.entries[len(l.entries)-max:]
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, err
	}
	l.file = f
	return l, nil
}

// Append records an entry; nil-safe so callers can hold a disabled log.
func (l *Log) Append(e Entry) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, e)
	if l.file != nil {
		if b, err := json.Marshal(e); err == nil {
			_, _ = l.file.Write(append(b, '\n'))
		}
	}
	if len(l.entries) > 2*l.max {
		l.entries = append([]Entry(nil), l.entries[len(l.entries)-l.max:]...)
		l.rewriteLocked()
	}
}

// rewriteLocked replaces the file with the in-memory entries (already bounded).
func (l *Log) rewriteLocked() {
	if l.path == "" {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(l.path), ".audit-*")
	if err != nil {
		return
	}
	ok := false
	defer func() {
		tmp.Close()
		if !ok {
			os.Remove(tmp.Name())
		}
	}()
	w := bufio.NewWriter(tmp)
	for _, e := range l.entries {
		b, err := json.Marshal(e)
		if err != nil {
			continue
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			return
		}
	}
	if err := w.Flush(); err != nil {
		return
	}
	if err := tmp.Sync(); err != nil {
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	if err := os.Rename(tmp.Name(), l.path); err != nil {
		return
	}
	ok = true
	// Reopen so the append handle points at the new inode.
	if l.file != nil {
		l.file.Close()
		if f, err := os.OpenFile(l.path, os.O_APPEND|os.O_WRONLY, 0o640); err == nil {
			l.file = f
		} else {
			l.file = nil
		}
	}
}

// Query returns newest-first entries with TS > after; user="" matches all.
// limit<=0 or >1000 clamps to the cap.
func (l *Log) Query(after int64, limit int, user string) []Entry {
	if l == nil {
		return nil
	}
	if limit <= 0 || limit > l.maxLimit {
		limit = l.maxLimit
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Entry, 0, min(limit, len(l.entries)))
	for i := len(l.entries) - 1; i >= 0 && len(out) < limit; i-- {
		e := l.entries[i]
		if e.TS <= after || (user != "" && e.User != user) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func (l *Log) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
}
