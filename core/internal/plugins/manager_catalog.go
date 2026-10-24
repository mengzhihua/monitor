package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Catalog is a versioned management snapshot. Revision changes only after a
// successful management action, never because metrics or process status change.
type Catalog struct {
	SchemaVersion int          `json:"schema_version"`
	Revision      uint64       `json:"revision"`
	Running       bool         `json:"running"`
	Plugins       []Status     `json:"plugins"`
	Diagnostics   []Diagnostic `json:"diagnostics"`
}

func cloneSpec(sp Spec) Spec {
	sp.Args = append([]string(nil), sp.Args...)
	sp.Env = append([]string(nil), sp.Env...)
	if sp.Manifest != nil {
		manifest := *sp.Manifest
		manifest.Args = append([]string(nil), manifest.Args...)
		manifest.Capabilities = append([]string(nil), manifest.Capabilities...)
		sp.Manifest = &manifest
	}
	return sp
}

func (m *Manager) discoverCatalog(ctx context.Context) ([]*plugin, []Diagnostic, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	disabled := map[string]bool{}
	for _, name := range m.opt.Disabled {
		disabled[name] = true
	}
	seen := map[string]bool{}
	list := []*plugin{}
	add := func(sp Spec, source string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if sp.Name == "" || seen[sp.Name] {
			return nil
		}
		seen[sp.Name] = true
		sp = cloneSpec(sp)
		sp.Source = source
		if sp.UpdateEvery <= 0 {
			sp.UpdateEvery = max(1, m.reg.Host.UpdateEvery)
		}
		if sp.Timeout <= 0 {
			sp.Timeout = max(10*sp.UpdateEvery, 60)
		}
		if !filepath.IsAbs(sp.Command) && m.opt.Dir != "" {
			if _, err := os.Stat(filepath.Join(m.opt.Dir, sp.Command)); err == nil {
				sp.Command = filepath.Join(m.opt.Dir, sp.Command)
			}
		}
		if strings.ContainsAny(sp.Command, `/\`) {
			if abs, err := filepath.Abs(sp.Command); err == nil {
				sp.Command = abs
			}
		} else if path, err := exec.LookPath(sp.Command); err == nil {
			sp.Command = path
		}
		locked := sp.Disabled || disabled[sp.Name]
		p := &plugin{spec: sp, locked: locked, enabled: !locked && !m.disabled[sp.Name], state: StateStarting, parser: newPluginParser(sp, m.reg), stderr: &stderrState{}}
		if !p.enabled {
			p.state = StateDisabled
		}
		if m.initErr != nil {
			p.enabled, p.state, p.err = false, StateFailed, "plugin state could not be loaded"
		}
		fingerprint, err := specFingerprint(ctx, sp)
		if err != nil {
			return err
		}
		p.fingerprint = fingerprint
		list = append(list, p)
		return nil
	}
	for _, sp := range m.specs {
		if err := add(sp, "configured"); err != nil {
			return nil, nil, err
		}
	}
	manifests, diagnostics, err := discoverManifests(ctx, m.opt.Dir)
	if err != nil {
		return nil, nil, err
	}
	for _, sp := range manifests {
		if err := add(sp, "manifest"); err != nil {
			return nil, nil, err
		}
	}
	legacy, err := discover(ctx, m.opt.Dir)
	if err != nil {
		return nil, nil, err
	}
	for _, sp := range legacy {
		if err := add(sp, "legacy"); err != nil {
			return nil, nil, err
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].spec.Name < list[j].spec.Name })
	if diagnostics == nil {
		diagnostics = []Diagnostic{}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return list, diagnostics, nil
}

// discover retains legacy executable discovery. A package directory reserves
// its name even if its manifest is invalid, so a broken package never silently
// falls back to a same-named legacy executable.
func discover(ctx context.Context, dir string) ([]Spec, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err != nil {
		return nil, nil
	}
	reserved := map[string]bool{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			reserved[entry.Name()] = true
		}
	}
	var out []Spec
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() {
			continue
		}
		name, base := entry.Name(), entry.Name()
		if runtime.GOOS == "windows" {
			ext := strings.ToLower(filepath.Ext(name))
			if ext == ".exe" || ext == ".bat" || ext == ".cmd" {
				base = strings.TrimSuffix(name, filepath.Ext(name))
			}
		}
		if !strings.HasSuffix(base, ".plugin") {
			continue
		}
		id := strings.TrimSuffix(base, ".plugin")
		if reserved[id] {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
			continue
		}
		out = append(out, Spec{Name: id, Command: filepath.Join(dir, name)})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Hash the executable as well as its settings, so replacing a binary at the
// same path is an observable rescan change. Neither args nor env leave this hash.
func specFingerprint(ctx context.Context, sp Spec) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	h := sha256.New()
	_ = json.NewEncoder(h).Encode(sp)
	if info, err := os.Stat(sp.Command); err == nil {
		fmt.Fprintf(h, "\n%d:%d:%d\n", info.Mode(), info.Size(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			if f, err := os.Open(sp.Command); err == nil {
				_, _ = io.Copy(h, discoveryReader{ctx: ctx, reader: f})
				_ = f.Close()
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (m *Manager) Catalog() Catalog {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Keep management fields and their revision in one short snapshot. Worker
	// state/metrics may advance independently; no file IO or worker join holds mu.
	return Catalog{SchemaVersion: 1, Revision: m.revision, Running: m.running,
		Diagnostics: append([]Diagnostic{}, m.diagnostics...), Plugins: snapshotPlugins(m.list)}
}

func (m *Manager) Status() []Status { return m.Catalog().Plugins }

func snapshotPlugins(list []*plugin) []Status {
	out := make([]Status, 0, len(list))
	for _, p := range list {
		p.mu.Lock()
		st := Status{Name: p.spec.Name, Command: p.spec.Command, State: p.state, PID: p.pid, Enabled: p.enabled,
			UpdateEvery: p.spec.UpdateEvery, Restarts: p.restart, Error: p.err, Source: p.spec.Source, Locked: p.locked}
		if !p.started.IsZero() {
			st.Started = p.started.Unix()
		}
		if p.spec.Manifest != nil {
			manifest := *p.spec.Manifest
			manifest.Command, manifest.Args = "", nil
			manifest.Capabilities = append([]string(nil), manifest.Capabilities...)
			st.Manifest = &manifest
		}
		parser := p.parser
		p.mu.Unlock()
		st.Stats, st.Variables, st.Functions = parser.Stats(), parser.Variables(), parser.Functions()
		st.Stats.LastError = redactProcessText(p.spec, st.Stats.LastError)
		st.Stderr = p.stderr.snapshot()
		out = append(out, st)
	}
	return out
}
