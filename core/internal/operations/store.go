// Package operations persists human acknowledgement independently of alarm evaluation.
package operations

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

var ErrConflict = errors.New("record changed; refresh before updating")

type Action struct {
	At     int64  `json:"at"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Note   string `json:"note"`
}

type Record struct {
	Problem      Target   `json:"problem"`
	ID           string   `json:"id"`
	Acknowledged bool     `json:"acknowledged"`
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
	mu    sync.Mutex
	path  string
	state state
}

// Open fails closed on corrupt state; it never silently discards operator notes.
func Open(dir string) (*Store, error) {
	s := &Store{state: state{Version: 1, Records: map[string]Record{}}}
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
	if err = json.Unmarshal(b, &s.state); err != nil {
		return nil, fmt.Errorf("operations state: %w", err)
	}
	if s.state.Version != 1 || s.state.Records == nil || len(s.state.Records) > 5000 {
		return nil, errors.New("invalid operations state")
	}
	for id, r := range s.state.Records {
		if r.ID != id || r.Revision == 0 || len(r.History) > 20 {
			return nil, errors.New("invalid operations record")
		}
	}
	return s, nil
}

func (s *Store) Persistent() bool { return s.path != "" }

func (s *Store) Get(id string) Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.state.Records[id]
	if !ok {
		return Record{ID: id, History: []Action{}}
	}
	r.History = append([]Action{}, r.History...)
	return r
}

// Update is optimistic and atomic: failed writes cannot change the visible state.
// Keep 20 actions per episode and 5000 episodes; refuse overflow rather than lose notes.
func (s *Store) Update(id, actor, action, note string, revision uint64, target Target) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.state.Records[id]
	if r.Revision != revision {
		return Record{}, ErrConflict
	}
	if action != "acknowledge" && action != "unacknowledge" && action != "comment" {
		return Record{}, errors.New("invalid action")
	}
	if len(note) > 2048 || (action == "comment" && note == "") {
		return Record{}, errors.New("note must be 1–2048 bytes for comments, at most 2048 bytes otherwise")
	}
	if r.Revision == 0 && len(s.state.Records) >= 5000 {
		return Record{}, errors.New("operations storage full (5000 episodes); archive state before adding records")
	}
	r.ID = id
	r.Problem = target
	r.Revision++
	if action != "comment" {
		r.Acknowledged = action == "acknowledge"
	}
	r.History = append(append([]Action{}, r.History...), Action{At: time.Now().Unix(), Actor: actor, Action: action, Note: note})
	if len(r.History) > 20 {
		r.History = r.History[len(r.History)-20:]
	}
	next := state{Version: 1, Records: make(map[string]Record, len(s.state.Records)+1)}
	for k, v := range s.state.Records {
		next.Records[k] = v
	}
	next.Records[id] = r
	if s.path != "" {
		b, err := json.Marshal(next)
		if err != nil {
			return Record{}, err
		}
		if len(b) > 64<<20 {
			return Record{}, errors.New("operations state exceeds 64 MiB")
		}
		f, err := os.CreateTemp(filepath.Dir(s.path), ".ack-*")
		if err != nil {
			return Record{}, err
		}
		defer os.Remove(f.Name())
		if _, err = f.Write(b); err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return Record{}, err
		}
		if closeErr != nil {
			return Record{}, closeErr
		}
		if err = os.Rename(f.Name(), s.path); err != nil {
			return Record{}, err
		}
	}
	s.state = next
	r.History = append([]Action{}, r.History...)
	return r, nil
}

// Recent includes handled episodes that no longer appear in active alarms.
func (s *Store) Recent() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, 0, len(s.state.Records))
	for _, r := range s.state.Records {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		var at, bt int64
		if len(a.History) > 0 {
			at = a.History[len(a.History)-1].At
		}
		if len(b.History) > 0 {
			bt = b.History[len(b.History)-1].At
		}
		if at != bt {
			return at > bt
		}
		return a.ID < b.ID
	})
	if len(out) > 100 {
		out = out[:100]
	}
	for i := range out {
		out[i].History = append([]Action{}, out[i].History...)
	}
	return out
}
