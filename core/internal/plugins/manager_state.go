package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"
)

const (
	maxControlStateBytes     = 1 << 20
	maxControlStateNameBytes = 1024
)

type controlState struct {
	SchemaVersion int             `json:"schema_version"`
	Revision      uint64          `json:"revision"`
	Disabled      map[string]bool `json:"disabled"`
}

func (m *Manager) loadState() error {
	if m.opt.StatePath == "" {
		return nil
	}
	info, err := os.Lstat(m.opt.StatePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("cannot inspect plugin control state")
	}
	if !info.Mode().IsRegular() || info.Size() > maxControlStateBytes {
		return errors.New("invalid plugin control state file")
	}
	file, err := os.Open(m.opt.StatePath)
	if err != nil {
		return errors.New("cannot open plugin control state")
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxControlStateBytes+1))
	decoder.DisallowUnknownFields()
	var state controlState
	if err := decoder.Decode(&state); err != nil {
		return errors.New("invalid plugin control state JSON")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("plugin control state must contain one JSON object")
	}
	if err := state.validate(); err != nil {
		return err
	}
	m.revision, m.disabled = state.Revision, state.Disabled
	return nil
}

// validate is shared by reads and writes. A successful control must never
// create state that prevents the same manager from restarting.
func (state controlState) validate() error {
	if state.SchemaVersion != 1 || state.Revision == 0 || state.Disabled == nil {
		return errors.New("unsupported or incomplete plugin control state")
	}
	for name := range state.Disabled {
		if name == "" || len(name) > maxControlStateNameBytes || !utf8.ValidString(name) {
			return errors.New("invalid plugin name in control state")
		}
	}
	return nil
}

// saveState commits a complete snapshot before a process is stopped or started.
// A failed write/rename leaves both the previous state and live workers intact.
func (m *Manager) saveState(revision uint64, disabled map[string]bool) error {
	if m.opt.StatePath == "" {
		return nil
	}
	state := controlState{SchemaVersion: 1, Revision: revision, Disabled: disabled}
	if err := state.validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("%w: cannot encode plugin state", ErrUnavailable)
	}
	payload = append(payload, '\n')
	// Count the actual encoded bytes, including JSON escaping and the newline.
	// Reject before touching disk or stopping a worker, retaining the last good
	// state when accumulated disabled overrides reach the reader's size limit.
	if len(payload) > maxControlStateBytes {
		return fmt.Errorf("%w: plugin control state exceeds 1 MiB", ErrUnavailable)
	}
	dir := filepath.Dir(m.opt.StatePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("%w: cannot create plugin state directory", ErrUnavailable)
	}
	file, err := os.CreateTemp(dir, ".plugins-state-*")
	if err != nil {
		return fmt.Errorf("%w: cannot create plugin state file", ErrUnavailable)
	}
	temp := file.Name()
	defer os.Remove(temp)
	defer file.Close()
	if _, err := file.Write(payload); err != nil {
		return fmt.Errorf("%w: cannot write plugin state", ErrUnavailable)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("%w: cannot flush plugin state", ErrUnavailable)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("%w: cannot close plugin state", ErrUnavailable)
	}
	if err := os.Rename(temp, m.opt.StatePath); err != nil {
		return fmt.Errorf("%w: cannot replace plugin state", ErrUnavailable)
	}
	// Rename is the commit point. Directory fsync is best-effort on platforms
	// that do not support it; reporting failure here would misreport a commit.
	if directory, err := os.Open(dir); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
