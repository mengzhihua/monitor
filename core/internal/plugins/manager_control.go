package plugins

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
)

var (
	ErrConflict    = errors.New("plugin catalog revision conflict")
	ErrUnavailable = errors.New("plugin manager unavailable")
	ErrNotFound    = errors.New("plugin not found")
	ErrPolicy      = errors.New("plugin action prohibited by policy")
	ErrInvalid     = errors.New("invalid plugin control request")
)

type ControlRequest struct {
	Name       string `json:"name"`
	Action     string `json:"action"`
	IfRevision uint64 `json:"if_revision"`
}

// Apply serializes lifecycle actions. Cancellation while queued or before the
// persistent commit has no side effects. After commit the manager completes the
// handoff, even if the caller disconnects, so disk and runtime cannot diverge.
func (m *Manager) Apply(ctx context.Context, req ControlRequest) (Catalog, error) {
	if err := m.acquire(ctx); err != nil {
		return m.Catalog(), err
	}
	defer m.release()
	if err := m.apply(ctx, req); err != nil {
		return m.Catalog(), err
	}
	return m.Catalog(), nil
}

func (m *Manager) apply(ctx context.Context, req ControlRequest) error {
	m.mu.Lock()
	running, revision := m.running, m.revision
	m.mu.Unlock()
	if !running || m.initErr != nil || m.runCtx.Err() != nil {
		return ErrUnavailable
	}
	if req.IfRevision != revision {
		return ErrConflict
	}
	if req.Action == "rescan" {
		if req.Name != "" {
			return ErrInvalid
		}
		return m.rescan(ctx)
	}
	if req.Name == "" || req.Action != "enable" && req.Action != "disable" && req.Action != "restart" {
		return ErrInvalid
	}
	var target *plugin
	for _, p := range m.list {
		if p.spec.Name == req.Name {
			target = p
			break
		}
	}
	if target == nil {
		return ErrNotFound
	}
	if target.locked && req.Action != "disable" {
		return ErrPolicy
	}
	target.mu.Lock()
	enabled := target.enabled
	target.mu.Unlock()
	if req.Action == "restart" && !enabled {
		return fmt.Errorf("%w: enable the plugin before restarting", ErrPolicy)
	}
	disabled := make(map[string]bool, len(m.disabled)+1)
	for name, value := range m.disabled {
		disabled[name] = value
	}
	switch req.Action {
	case "enable":
		if enabled && !disabled[req.Name] {
			return nil
		}
		delete(disabled, req.Name)
	case "disable":
		if !enabled && (disabled[req.Name] || target.locked) {
			return nil
		}
		disabled[req.Name] = true
	}
	if err := m.commit(ctx, disabled); err != nil {
		return err
	}
	m.stopWorker(target)
	target.mu.Lock()
	target.enabled = req.Action != "disable"
	if !target.enabled {
		target.state, target.err = StateDisabled, ""
	}
	if req.Action == "restart" {
		target.restart++
	}
	target.mu.Unlock()
	m.startWorker(target)
	return nil
}

func (m *Manager) commit(ctx context.Context, disabled map[string]bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.runCtx.Err() != nil {
		return ErrUnavailable
	}
	if m.revision == math.MaxUint64 {
		return fmt.Errorf("%w: catalog revision exhausted", ErrUnavailable)
	}
	revision := m.revision + 1
	if err := m.saveState(revision, disabled); err != nil {
		return err
	}
	m.mu.Lock()
	m.revision, m.disabled = revision, disabled
	m.mu.Unlock()
	return nil
}

func (m *Manager) rescan(ctx context.Context) error {
	next, diagnostics := m.discoverCatalog()
	previous := make(map[string]*plugin, len(m.list))
	for _, p := range m.list {
		previous[p.spec.Name] = p
	}
	changed := !reflect.DeepEqual(m.diagnostics, diagnostics)
	var replacements []*plugin
	for i, p := range next {
		old := previous[p.spec.Name]
		if old != nil && old.fingerprint == p.fingerprint {
			next[i] = old
		} else {
			changed = true
			if old != nil {
				old.mu.Lock()
				p.restart = old.restart + 1
				old.mu.Unlock()
			}
			replacements = append(replacements, p)
		}
		delete(previous, p.spec.Name)
	}
	if len(previous) > 0 {
		changed = true
	}
	if !changed {
		return nil
	}
	if err := m.commit(ctx, m.disabled); err != nil {
		return err
	}
	// Stop every removed or changed worker first, including all of its children.
	nextByName := make(map[string]*plugin, len(next))
	for _, p := range next {
		nextByName[p.spec.Name] = p
	}
	var stopped []*plugin
	for _, old := range m.list {
		if nextByName[old.spec.Name] != old {
			if old.cancel != nil {
				old.cancel()
			}
			stopped = append(stopped, old)
		}
	}
	for _, old := range stopped {
		m.stopWorker(old)
	}
	m.mu.Lock()
	m.list, m.diagnostics = next, diagnostics
	m.mu.Unlock()
	for _, p := range replacements {
		m.startWorker(p)
	}
	return nil
}
