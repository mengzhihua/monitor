package operations

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const ViewLimit = 10
const maxViewAccounts = 1000
const maxViewsBytes = 16 << 20
const maxViewRevision = 1<<53 - 1 // JSON integers must round-trip through browsers.

var ErrInvalidView = errors.New("invalid saved views")
var ErrViewCapacity = errors.New("saved views capacity reached")

// SavedView contains filters only; it never captures metrics, credentials or notes.
type SavedView struct {
	Name           string `json:"name"`
	Query          string `json:"query"`
	Severity       string `json:"severity"`
	NodeStatus     string `json:"nodeStatus"`
	PendingOnly    bool   `json:"pendingOnly"`
	OwnerFilter    string `json:"ownerFilter"`
	ProgressFilter string `json:"progressFilter"`
}

type ViewCollection struct {
	Revision uint64      `json:"revision"`
	Views    []SavedView `json:"views"`
}

type viewState struct {
	Version  int                       `json:"version"`
	Accounts map[string]ViewCollection `json:"accounts"`
}

type ViewStore struct {
	mu    sync.Mutex
	path  string
	state viewState
}

func validViewKey(key string) bool {
	b, err := hex.DecodeString(key)
	return err == nil && len(b) == 32 && key == strings.ToLower(key)
}

func validateViews(views []SavedView) error {
	if views == nil || len(views) > ViewLimit {
		return ErrInvalidView
	}
	names := map[string]bool{}
	for _, v := range views {
		if v.Name == "" || strings.TrimSpace(v.Name) != v.Name || !utf8.ValidString(v.Name) || utf8.RuneCountInString(v.Name) > 40 || strings.IndexFunc(v.Name, unicode.IsControl) >= 0 || names[v.Name] ||
			len(v.Query) > 1024 || !utf8.ValidString(v.Query) || strings.IndexFunc(v.Query, unicode.IsControl) >= 0 ||
			!slices.Contains([]string{"all", "WARNING", "CRITICAL"}, v.Severity) ||
			!slices.Contains([]string{"all", "live", "stale", "offline"}, v.NodeStatus) ||
			!slices.Contains([]string{"all", "mine", "unassigned", "assigned"}, v.OwnerFilter) ||
			!slices.Contains([]string{"all", StatusOpen, StatusInvestigating, StatusWatching}, v.ProgressFilter) {
			return ErrInvalidView
		}
		names[v.Name] = true
	}
	return nil
}

// OpenViews fails closed on damaged files, preserving the original for recovery.
func OpenViews(dir string) (*ViewStore, error) {
	s := &ViewStore{state: viewState{Version: 1, Accounts: map[string]ViewCollection{}}}
	if dir == "" {
		return s, nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	s.path = filepath.Join(dir, "views.json")
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxViewsBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxViewsBytes || json.Unmarshal(b, &s.state) != nil || s.state.Version != 1 || s.state.Accounts == nil || len(s.state.Accounts) > maxViewAccounts {
		return nil, errors.New("invalid saved views file")
	}
	for key, c := range s.state.Accounts {
		if !validViewKey(key) || c.Revision == 0 || c.Revision > maxViewRevision || validateViews(c.Views) != nil {
			return nil, errors.New("invalid saved views account")
		}
	}
	return s, nil
}

func (s *ViewStore) Persistent() bool { return s.path != "" }

func copyViews(c ViewCollection) ViewCollection {
	c.Views = append([]SavedView{}, c.Views...)
	return c
}

func (s *ViewStore) Get(key string) ViewCollection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return copyViews(s.state.Accounts[key])
}

// Replace compares the entire collection revision; deletes keep a tombstone so
// a stale browser cannot resurrect a removed collection with revision zero.
func (s *ViewStore) Replace(key string, revision uint64, views []SavedView) (ViewCollection, error) {
	if !validViewKey(key) || validateViews(views) != nil {
		return ViewCollection{}, ErrInvalidView
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.state.Accounts[key]
	if current.Revision != revision {
		return ViewCollection{}, ErrConflict
	}
	if (!exists && len(s.state.Accounts) >= maxViewAccounts) || revision >= maxViewRevision {
		return ViewCollection{}, ErrViewCapacity
	}
	updated := ViewCollection{Revision: revision + 1, Views: append([]SavedView{}, views...)}
	next := viewState{Version: 1, Accounts: make(map[string]ViewCollection, len(s.state.Accounts)+1)}
	for k, c := range s.state.Accounts {
		next.Accounts[k] = c
	}
	next.Accounts[key] = updated
	b, err := json.Marshal(next)
	if err != nil {
		return ViewCollection{}, err
	}
	if len(b) > maxViewsBytes {
		return ViewCollection{}, ErrViewCapacity
	}
	if s.path != "" {
		if err := writeViews(s.path, b); err != nil {
			return ViewCollection{}, fmt.Errorf("persist saved views: %w", err)
		}
	}
	s.state = next
	return copyViews(updated), nil
}

func writeViews(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".views-*") // mode 0600
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
