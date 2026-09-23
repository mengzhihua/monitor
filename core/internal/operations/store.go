// Package operations persists human acknowledgement independently of alarm evaluation.
package operations

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrConflict = errors.New("record changed; refresh before updating")
var ErrInvalidChange = errors.New("invalid handling change")

const (
	StatusOpen          = "open"
	StatusInvestigating = "investigating"
	StatusWatching      = "watching"
)

// Change alters human workflow only, never the alarm's evaluated severity.
type Change struct {
	Action   string `json:"action"`
	Note     string `json:"note"`
	Assignee string `json:"assignee,omitempty"`
	Status   string `json:"status,omitempty"`
}

func ValidAssignee(name string) bool {
	return name != "" && len(name) <= 128 && utf8.ValidString(name) && strings.TrimSpace(name) == name && strings.IndexFunc(name, unicode.IsControl) < 0
}

func validStatus(status string) bool {
	return status == StatusOpen || status == StatusInvestigating || status == StatusWatching
}

func (c Change) Validate() error {
	if len(c.Note) > 2048 || !utf8.ValidString(c.Note) {
		return fmt.Errorf("%w: note must be at most 2048 UTF-8 bytes", ErrInvalidChange)
	}
	switch c.Action {
	case "acknowledge", "unacknowledge", "comment", "unassign":
		if c.Assignee != "" || c.Status != "" || (c.Action == "comment" && strings.TrimSpace(c.Note) == "") {
			return ErrInvalidChange
		}
	case "assign":
		if !ValidAssignee(c.Assignee) || c.Status != "" {
			return ErrInvalidChange
		}
	case "progress":
		if !validStatus(c.Status) || c.Assignee != "" {
			return ErrInvalidChange
		}
	default:
		return ErrInvalidChange
	}
	return nil
}

type Action struct {
	At               int64  `json:"at"`
	Actor            string `json:"actor"`
	Action           string `json:"action"`
	Note             string `json:"note"`
	PreviousAssignee string `json:"previous_assignee,omitempty"`
	Assignee         string `json:"assignee,omitempty"`
	PreviousStatus   string `json:"previous_status,omitempty"`
	Status           string `json:"status,omitempty"`
}

type Record struct {
	Problem      Target   `json:"problem"`
	ID           string   `json:"id"`
	Acknowledged bool     `json:"acknowledged"`
	Assignee     string   `json:"assignee"`
	Status       string   `json:"status"`
	Revision     uint64   `json:"revision"`
	History      []Action `json:"history"`
}

// Target keeps the operator's context available after an alarm has recovered.
type Target struct {
	Node     string `json:"node"`
	Hostname string `json:"hostname"`
	Chart    string `json:"chart"`
	Name     string `json:"name"`
	Severity string `json:"severity"`
	Since    int64  `json:"since"`
}

type state struct {
	Version int               `json:"version"`
	Records map[string]Record `json:"records"`
}

type Store struct {
	mu             sync.Mutex
	path           string
	state          state
	historyVersion string
	historyOrder   []Record // read-only under mu; invalidated after successful writes
}

// Open fails closed on corrupt state; it never silently discards operator notes.
func Open(dir string) (*Store, error) {
	s := &Store{state: state{Version: 2, Records: map[string]Record{}}}
	if dir == "" {
		return s, nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	s.path = filepath.Join(dir, "acknowledgements.json")
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) > 64<<20 {
		return nil, errors.New("operations state exceeds 64 MiB")
	}
	// Decode into a zero value so a missing envelope cannot inherit the valid
	// defaults reserved for a brand-new store (for example, {} or null).
	var disk state
	if err = json.Unmarshal(b, &disk); err != nil {
		return nil, fmt.Errorf("operations state: %w", err)
	}
	if (disk.Version != 1 && disk.Version != 2) || disk.Records == nil || len(disk.Records) > 5000 {
		return nil, errors.New("invalid operations state")
	}
	for id, r := range disk.Records {
		if disk.Version == 1 {
			r.Status = StatusOpen
		}
		if r.ID != id || r.Revision == 0 || len(r.History) > 20 {
			return nil, errors.New("invalid operations record")
		}
		if !validStatus(r.Status) || (r.Assignee != "" && !ValidAssignee(r.Assignee)) {
			return nil, errors.New("invalid operations workflow")
		}
		disk.Records[id] = r
	}
	// The next successful write upgrades the file. Older binaries reject v2
	// instead of silently dropping assignments and progress.
	disk.Version = 2
	s.state = disk
	return s, nil
}

func (s *Store) Persistent() bool { return s.path != "" }

func (s *Store) Get(id string) Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.state.Records[id]
	if !ok {
		return Record{ID: id, Status: StatusOpen, History: []Action{}}
	}
	r.History = append([]Action{}, r.History...)
	return r
}

// Update is optimistic and atomic: failed writes cannot change the visible state.
// Keep 20 actions per episode and 5000 episodes; refuse overflow rather than lose notes.
func (s *Store) Update(id, actor, action, note string, revision uint64, target Target) (Record, error) {
	return s.Apply(id, actor, Change{Action: action, Note: note}, revision, target)
}

// BatchItem pins the alarm episode and the handling revision seen by the caller.
type BatchItem struct {
	ID       string
	Revision uint64
	Target   Target
}

const BatchLimit = 50

func (s *Store) Apply(id, actor string, change Change, revision uint64, target Target) (Record, error) {
	records, err := s.ApplyBatch([]BatchItem{{ID: id, Revision: revision, Target: target}}, actor, change)
	if err != nil {
		return Record{}, err
	}
	return records[0], nil
}

// ApplyBatch validates every member under the same lock and publishes one state
// after one successful write. A conflict or storage failure changes no records.
func (s *Store) ApplyBatch(items []BatchItem, actor string, change Change) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := change.Validate(); err != nil {
		return nil, err
	}
	if len(items) == 0 || len(items) > BatchLimit {
		return nil, ErrInvalidChange
	}
	seen := make(map[string]bool, len(items))
	added := 0
	for _, item := range items {
		if item.ID == "" || seen[item.ID] {
			return nil, ErrInvalidChange
		}
		seen[item.ID] = true
		current, exists := s.state.Records[item.ID]
		if current.Revision != item.Revision {
			return nil, ErrConflict
		}
		if !exists {
			added++
		}
	}
	if len(s.state.Records)+added > 5000 {
		return nil, errors.New("operations storage full (5000 episodes); archive state before adding records")
	}
	next := state{Version: 2, Records: make(map[string]Record, len(s.state.Records)+added)}
	for k, v := range s.state.Records {
		next.Records[k] = v
	}
	records := make([]Record, 0, len(items))
	now := time.Now().Unix()
	for _, item := range items {
		r := next.Records[item.ID]
		r.ID, r.Problem = item.ID, item.Target
		r.Revision++
		if r.Status == "" {
			r.Status = StatusOpen
		}
		a := Action{At: now, Actor: actor, Action: change.Action, Note: strings.TrimSpace(change.Note)}
		switch change.Action {
		case "acknowledge", "unacknowledge":
			r.Acknowledged = change.Action == "acknowledge"
		case "assign", "unassign":
			a.PreviousAssignee, a.Assignee = r.Assignee, change.Assignee
			r.Assignee = change.Assignee
		case "progress":
			a.PreviousStatus, a.Status = r.Status, change.Status
			r.Status = change.Status
		}
		r.History = append(append([]Action{}, r.History...), a)
		if len(r.History) > 20 {
			r.History = r.History[len(r.History)-20:]
		}
		next.Records[item.ID] = r
		r.History = append([]Action{}, r.History...)
		records = append(records, r)
	}
	if s.path != "" {
		b, err := json.Marshal(next)
		if err != nil {
			return nil, err
		}
		if len(b) > 64<<20 {
			return nil, errors.New("operations state exceeds 64 MiB")
		}
		f, err := os.CreateTemp(filepath.Dir(s.path), ".ack-*")
		if err != nil {
			return nil, err
		}
		defer os.Remove(f.Name())
		if _, err = f.Write(b); err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if err = os.Rename(f.Name(), s.path); err != nil {
			return nil, err
		}
	}
	s.state = next
	s.historyVersion = ""
	s.historyOrder = nil
	return records, nil
}

// Recent includes handled episodes that no longer appear in active alarms.
func (s *Store) Recent() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	ordered := s.orderedHistoryLocked()
	out := make([]Record, min(100, len(ordered)))
	copy(out, ordered)
	for i := range out {
		out[i].History = append([]Action{}, out[i].History...)
	}
	return out
}
