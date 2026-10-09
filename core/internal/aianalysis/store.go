// Package aianalysis stores per-identity model settings and sends bounded,
// read-only analysis requests to OpenAI-compatible chat completion endpoints.
package aianalysis

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

var (
	ErrConflict     = errors.New("AI settings changed; reload before saving")
	ErrInvalid      = errors.New("invalid AI settings or request")
	ErrUnavailable  = errors.New("AI settings are unavailable")
	ErrBusy         = errors.New("AI analysis is already running; try again later")
	ErrUnconfigured = errors.New("AI analysis is not configured")
	ErrProvider     = errors.New("AI provider request failed")
)

const (
	maxAccounts  = 256
	maxStoreSize = 2 << 20
	// Revisions travel through browser JSON numbers, so they must remain exact
	// in IEEE-754 doubles as well as Go integers.
	maxRevision uint64 = 1<<53 - 1
)

type Config struct {
	BaseURL      string `json:"base_url"`
	Model        string `json:"model"`
	APIKey       string `json:"-"`
	AllowPrivate bool   `json:"allow_private"`
	Revision     uint64 `json:"revision"`
}

type PublicConfig struct {
	BaseURL      string `json:"base_url"`
	Model        string `json:"model"`
	HasAPIKey    bool   `json:"has_api_key"`
	AllowPrivate bool   `json:"allow_private"`
	Revision     uint64 `json:"revision"`
}

type Update struct {
	BaseURL      string  `json:"base_url"`
	Model        string  `json:"model"`
	APIKey       *string `json:"api_key"`
	ClearAPIKey  bool    `json:"clear_api_key"`
	AllowPrivate bool    `json:"allow_private"`
	Revision     uint64  `json:"revision"`
}

type diskConfig struct {
	BaseURL      string `json:"base_url,omitempty"`
	Model        string `json:"model,omitempty"`
	APIKey       string `json:"api_key,omitempty"`
	AllowPrivate bool   `json:"allow_private,omitempty"`
	Revision     uint64 `json:"revision"`
}

type diskState struct {
	Version  int                   `json:"version"`
	Accounts map[string]diskConfig `json:"accounts"`
}

// Store is safe for concurrent use. An empty directory enables memory-only
// settings; otherwise changes must reach disk before becoming visible.
type Store struct {
	mu       sync.Mutex
	path     string
	configs  map[string]Config
	active   map[string]bool
	inFlight int
	resolver ipResolver
	dialer   contextDialer
	syncDir  func(string) error
	// A post-rename sync failure leaves durability uncertain. Refuse all
	// further operations until reopening the actual on-disk state.
	unavailable bool
}

func Open(dir string) (*Store, error) {
	s := &Store{
		configs: make(map[string]Config), active: make(map[string]bool),
		resolver: net.DefaultResolver, dialer: &net.Dialer{Timeout: dialTimeout},
		syncDir: syncDirectory,
	}
	if dir == "" {
		return s, nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, ErrUnavailable
	}
	s.path = filepath.Join(dir, "ai-settings.json")
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxStoreSize {
		return nil, ErrUnavailable
	}
	f, err := os.Open(s.path)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer f.Close()
	openedInfo, err := f.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, ErrUnavailable
	}
	if err := f.Chmod(0600); err != nil {
		return nil, ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(f, maxStoreSize+1))
	if err != nil || len(data) > maxStoreSize {
		return nil, ErrUnavailable
	}
	var state diskState
	if err := json.Unmarshal(data, &state); err != nil || state.Version != 1 || state.Accounts == nil || len(state.Accounts) > maxAccounts {
		return nil, ErrUnavailable
	}
	for owner, c := range state.Accounts {
		if !validOwner(owner) || c.Revision == 0 || c.Revision > maxRevision {
			return nil, ErrUnavailable
		}
		cfg := Config{c.BaseURL, c.Model, c.APIKey, c.AllowPrivate, c.Revision}
		if c.BaseURL == "" {
			if c.Model != "" || c.APIKey != "" || c.AllowPrivate {
				return nil, ErrUnavailable
			}
		} else if err := validateConfig(&cfg); err != nil {
			return nil, ErrUnavailable
		}
		s.configs[owner] = cfg
	}
	return s, nil
}

func (s *Store) Persistent() bool { return s.path != "" }

// Available is false after a persistence failure whose rename already
// committed. API handlers should report an unavailable response in that case.
func (s *Store) Available() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.unavailable
}

func (s *Store) Get(owner string) PublicConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unavailable {
		return PublicConfig{}
	}
	return public(s.configs[owner])
}

func (s *Store) Save(owner string, u Update, admin bool) (PublicConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unavailable {
		return PublicConfig{}, ErrUnavailable
	}
	if !validOwner(owner) || u.Revision > maxRevision || (u.APIKey != nil && u.ClearAPIKey) || (u.AllowPrivate && !admin) {
		return PublicConfig{}, ErrInvalid
	}
	old := s.configs[owner]
	if old.Revision != u.Revision {
		return PublicConfig{}, ErrConflict
	}
	if old.Revision == maxRevision {
		return PublicConfig{}, ErrUnavailable
	}
	key := old.APIKey
	if u.ClearAPIKey {
		key = ""
	} else if u.APIKey != nil {
		key = *u.APIKey
	}
	cfg := Config{u.BaseURL, u.Model, key, u.AllowPrivate, old.Revision + 1}
	if err := validateConfig(&cfg); err != nil {
		return PublicConfig{}, err
	}
	// An unchanged secret must never follow a newly entered provider URL.
	if old.APIKey != "" && old.BaseURL != cfg.BaseURL && u.APIKey == nil && !u.ClearAPIKey {
		return PublicConfig{}, ErrInvalid
	}
	if err := s.replaceLocked(owner, cfg); err != nil {
		return PublicConfig{}, err
	}
	return public(cfg), nil
}

func (s *Store) Delete(owner string, revision uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unavailable {
		return ErrUnavailable
	}
	if !validOwner(owner) || revision > maxRevision {
		return ErrInvalid
	}
	old := s.configs[owner]
	if old.Revision != revision {
		return ErrConflict
	}
	if old.Revision == maxRevision {
		return ErrUnavailable
	}
	return s.replaceLocked(owner, Config{Revision: revision + 1})
}

func (s *Store) replaceLocked(owner string, cfg Config) error {
	if _, exists := s.configs[owner]; !exists && len(s.configs) >= maxAccounts {
		return ErrUnavailable
	}
	state := diskState{Version: 1, Accounts: make(map[string]diskConfig, len(s.configs)+1)}
	for id, c := range s.configs {
		state.Accounts[id] = diskConfig{c.BaseURL, c.Model, c.APIKey, c.AllowPrivate, c.Revision}
	}
	state.Accounts[owner] = diskConfig{cfg.BaseURL, cfg.Model, cfg.APIKey, cfg.AllowPrivate, cfg.Revision}
	data, err := json.Marshal(state)
	if err != nil || len(data) > maxStoreSize {
		return ErrUnavailable
	}
	if s.path != "" {
		committed, err := atomicWrite(s.path, data, s.syncDir)
		if err != nil {
			if committed {
				s.unavailable = true
			}
			return ErrUnavailable
		}
	}
	s.configs[owner] = cfg
	return nil
}

func atomicWrite(path string, data []byte, syncDir func(string) error) (bool, error) {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".ai-settings-*")
	if err != nil {
		return false, err
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return false, err
	}
	if err = f.Sync(); err != nil {
		return false, err
	}
	if err = f.Close(); err != nil {
		return false, err
	}
	if err = os.Rename(name, path); err != nil {
		return false, err
	}
	return true, syncDir(dir)
}

func syncDirectory(dir string) error {
	// Windows does not support flushing a directory handle opened by os.Open.
	// The temporary file itself has already been synced before replacement.
	if runtime.GOOS == "windows" {
		return nil
	}
	// fsync the containing directory so the rename survives a restart.
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func public(c Config) PublicConfig {
	return PublicConfig{c.BaseURL, c.Model, c.APIKey != "", c.AllowPrivate, c.Revision}
}

func validOwner(owner string) bool {
	if len(owner) != 64 || owner != strings.ToLower(owner) {
		return false
	}
	_, err := hex.DecodeString(owner)
	return err == nil
}

func noControls(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validateConfig(c *Config) error {
	if !noControls(c.Model) {
		return ErrInvalid
	}
	c.Model = strings.TrimSpace(c.Model)
	if c.Model == "" || len(c.Model) > 128 || !noControls(c.Model) || len(c.APIKey) > 8192 || !noControls(c.APIKey) {
		return ErrInvalid
	}
	base, err := validateBaseURL(c.BaseURL, c.AllowPrivate)
	if err != nil {
		return err
	}
	c.BaseURL = base
	return nil
}
