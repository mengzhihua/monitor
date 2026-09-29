package health

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

const RuleOverrideLimit = 1000
const maxRuleConfigBytes = 4 << 20

var ErrRuleConflict = errors.New("alert rules changed or rule already exists")
var ErrRuleNotFound = errors.New("alert rule not found")
var ErrRuleInvalid = errors.New("invalid alert rule mutation")
var ErrRuleCapacity = errors.New("alert rule override capacity reached")

// RuleConfigSnapshot includes the effective rules and their original file-based
// definitions. Revision is opaque and changes on every mutation and restart.
type RuleConfigSnapshot struct {
	Revision      string   `json:"revision"`
	Persistent    bool     `json:"persistent"`
	Rules         []*Rule  `json:"rules"`
	Base          []*Rule  `json:"base"`
	Removed       []string `json:"removed"`
	Overridden    []string `json:"overridden"`
	Templates     []string `json:"templates,omitempty"`      // matched template names (agent overlay)
	TemplateRules []string `json:"template_rules,omitempty"` // rule names supplied by templates
	Invalid       []string `json:"invalid,omitempty"`        // runtime rules skipped under current macros
}

// OverlayLayer is the hub-pushed template overlay: rules, macros and base-rule
// tombstones resolved for this node. Macros layer over the global health.macros
// (global < template < rule-level) when compiling both overlay and runtime
// rules. The runtime layer still wins over overlay rules of the same name; a
// runtime Restore does not resurrect a rule hidden by the overlay's Removed.
type OverlayLayer struct {
	Rev        int64             `json:"rev"`
	Macros     map[string]string `json:"macros,omitempty"`
	Rules      []RuleSpec        `json:"rules,omitempty"`
	Removed    []string          `json:"removed,omitempty"`
	Templates  []string          `json:"templates,omitempty"`   // provenance: matched template names
	RuleSource map[string]string `json:"rule_source,omitempty"` // rule name -> template name
}

type overlayFile struct {
	Version int          `json:"version"`
	Overlay OverlayLayer `json:"overlay"`
}

type RuleMutation struct {
	Upserts    []RuleSpec
	Delete     []string
	Restore    []string
	CreateOnly bool
}

type ruleConfigState struct {
	Version   int        `json:"version"`
	Counter   uint64     `json:"counter"`
	Overrides []RuleSpec `json:"overrides"`
	Removed   []string   `json:"removed"`
}

// The engine's tickMu serializes writers and evaluation. State is published
// under engine.mu only after the replacement file has been written successfully.
type ruleConfigStore struct {
	path        string
	overlayPath string
	epoch       string
	base        []*Rule
	state       ruleConfigState
	macros      map[string]string // global {$NAME} user macros
	overlay     OverlayLayer      // hub template overlay (Rev 0 = none)
	invalid     []string          // runtime overrides invalid under overlay macros
}

func cloneRuleSpec(spec RuleSpec) RuleSpec {
	if spec.Labels != nil {
		labels := make(map[string]string, len(spec.Labels))
		for key, value := range spec.Labels {
			labels[key] = value
		}
		spec.Labels = labels
	}
	if spec.Macros != nil {
		macros := make(map[string]string, len(spec.Macros))
		for key, value := range spec.Macros {
			macros[key] = value
		}
		spec.Macros = macros
	}
	return spec
}

func cloneRule(rule *Rule) *Rule {
	if rule == nil {
		return nil
	}
	copy := *rule
	copy.Spec = cloneRuleSpec(rule.Spec)
	if rule.Lookup != nil {
		lookup := *rule.Lookup
		lookup.Dimensions = append([]string(nil), rule.Lookup.Dimensions...)
		copy.Lookup = &lookup
	}
	copy.Calc = cloneRuleExpr(rule.Calc)
	copy.Warn = cloneRuleExpr(rule.Warn)
	copy.Crit = cloneRuleExpr(rule.Crit)
	copy.Recovery = cloneRuleExpr(rule.Recovery)
	return &copy
}

func cloneRuleExpr(expr *Expr) *Expr {
	if expr == nil {
		return nil
	}
	copy := *expr
	// Parsed nodes are private and immutable; Vars exposes its backing slice.
	copy.vars = append([]string(nil), expr.vars...)
	return &copy
}

func cloneRules(rules []*Rule) []*Rule {
	out := make([]*Rule, len(rules))
	for i, rule := range rules {
		out[i] = cloneRule(rule)
	}
	return out
}

func openRuleConfig(dir string, base []*Rule, macros map[string]string) (*ruleConfigStore, []*Rule, error) {
	var epoch [16]byte
	if _, err := rand.Read(epoch[:]); err != nil {
		return nil, nil, err
	}
	for _, rule := range base {
		if rule == nil {
			return nil, nil, fmt.Errorf("%w: nil base rule", ErrRuleInvalid)
		}
	}
	s := &ruleConfigStore{epoch: hex.EncodeToString(epoch[:]), base: cloneRules(Merge(base)), macros: macros,
		state: ruleConfigState{Version: 1, Overrides: []RuleSpec{}, Removed: []string{}}}
	if dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, nil, err
		}
		s.path = filepath.Join(dir, "alert-rules.json")
		f, err := os.Open(s.path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
		if err == nil {
			defer f.Close()
			b, err := io.ReadAll(io.LimitReader(f, maxRuleConfigBytes+1))
			if err != nil {
				return nil, nil, err
			}
			var disk ruleConfigState
			if len(b) > maxRuleConfigBytes || json.Unmarshal(b, &disk) != nil || disk.Version != 1 || disk.Overrides == nil || disk.Removed == nil || (disk.Counter == 0 && len(disk.Overrides)+len(disk.Removed) != 0) {
				return nil, nil, fmt.Errorf("%w: damaged alert rules file", ErrRuleInvalid)
			}
			s.state = disk
		}
	}
	if dir != "" {
		s.overlayPath = filepath.Join(dir, "template-overlay.json")
		if ov, err := loadOverlay(s.overlayPath); err != nil {
			return nil, nil, err
		} else if ov != nil {
			s.overlay = *ov
		}
	}
	// Runtime overrides invalid under the persisted overlay macros are
	// tolerated on load (listed in the store's invalid set); base and
	// template compile errors still fail startup.
	rules, invalid, err := effectiveRules(s.base, s.state, s.effectiveMacros(), &s.overlay, true)
	if err != nil {
		return nil, nil, err
	}
	s.invalid = invalid
	return s, rules, nil
}

func loadOverlay(path string) (*OverlayLayer, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxRuleConfigBytes+1))
	if err != nil {
		return nil, err
	}
	var disk overlayFile
	if len(b) > maxRuleConfigBytes || json.Unmarshal(b, &disk) != nil || disk.Version != 1 {
		return nil, fmt.Errorf("%w: damaged template overlay file", ErrRuleInvalid)
	}
	return &disk.Overlay, nil
}

// effectiveMacros merges global and template-overlay macros; the overlay wins.
func (s *ruleConfigStore) effectiveMacros() map[string]string {
	if len(s.overlay.Macros) == 0 {
		return s.macros
	}
	out := make(map[string]string, len(s.macros)+len(s.overlay.Macros))
	for k, v := range s.macros {
		out[k] = v
	}
	for k, v := range s.overlay.Macros {
		out[k] = v
	}
	return out
}

// effectiveRules merges base + template overlay + runtime overrides. Runtime
// overrides that no longer compile under the current macro set are skipped
// and returned in invalid (template rules still reject the whole merge).
func effectiveRules(base []*Rule, state ruleConfigState, macros map[string]string, overlay *OverlayLayer, tolerateRuntime bool) ([]*Rule, []string, error) {
	var invalid []string
	if len(state.Overrides)+len(state.Removed) > RuleOverrideLimit {
		return nil, invalid, ErrRuleCapacity
	}
	var tplRules []*Rule
	seenTpl := make(map[string]bool)
	if overlay != nil {
		for _, spec := range overlay.Rules {
			if seenTpl[spec.Name] {
				return nil, invalid, fmt.Errorf("%w: duplicate template rule %q", ErrRuleInvalid, spec.Name)
			}
			seenTpl[spec.Name] = true
			src := "template"
			if name := overlay.RuleSource[spec.Name]; name != "" {
				src = "template:" + name
			}
			rule, err := CompileWith(cloneRuleSpec(spec), src, macros)
			if err != nil {
				return nil, invalid, fmt.Errorf("%w: %v", ErrRuleInvalid, err)
			}
			tplRules = append(tplRules, rule)
		}
	}
	tplRemoved := make(map[string]bool)
	if overlay != nil {
		for _, name := range overlay.Removed {
			if strings.TrimSpace(name) == "" || tplRemoved[name] {
				return nil, invalid, fmt.Errorf("%w: duplicate or empty removed template rule", ErrRuleInvalid)
			}
			tplRemoved[name] = true
		}
	}
	seen := make(map[string]bool)
	overrides := make([]*Rule, 0, len(state.Overrides))
	for _, spec := range state.Overrides {
		if seen[spec.Name] {
			return nil, invalid, fmt.Errorf("%w: duplicate rule %q", ErrRuleInvalid, spec.Name)
		}
		seen[spec.Name] = true
		rule, err := CompileWith(cloneRuleSpec(spec), "api", macros)
		if err != nil {
			// Only macro-resolution failures are tolerated: a runtime
			// override may reference a macro the new overlay removed.
			// Other compile errors (e.g. damaged persisted files) must still
			// fail so corrupt state is not silently dropped.
			if !tolerateRuntime || !strings.Contains(err.Error(), "macro") {
				return nil, invalid, fmt.Errorf("%w: %v", ErrRuleInvalid, err)
			}
			slog.Warn("health: skipping invalid runtime rule under new overlay", "rule", spec.Name, "err", err)
			invalid = append(invalid, spec.Name)
			continue
		}
		overrides = append(overrides, rule)
	}
	removed := make(map[string]bool, len(state.Removed))
	for _, name := range state.Removed {
		if strings.TrimSpace(name) == "" || seen[name] || removed[name] {
			return nil, invalid, fmt.Errorf("%w: duplicate or empty removed rule", ErrRuleInvalid)
		}
		removed[name] = true
	}
	sort.Slice(overrides, func(i, j int) bool { return overrides[i].Spec.Name < overrides[j].Spec.Name })
	merged := Merge(base, tplRules, overrides)
	rules := make([]*Rule, 0, len(merged))
	for _, rule := range merged {
		if !removed[rule.Spec.Name] && !tplRemoved[rule.Spec.Name] {
			rules = append(rules, rule)
		}
	}
	return rules, invalid, nil
}

func (s *ruleConfigStore) revision() string {
	// The overlay is part of the effective rule set, so its revision is part
	// of the snapshot revision: a SetOverlay invalidates earlier CAS tokens.
	return s.epoch + ":" + strconv.FormatUint(s.state.Counter, 10) + ":" + strconv.FormatInt(s.overlay.Rev, 10)
}

// RulesConfig returns an isolated, internally consistent revision and rule set.
func (e *Engine) RulesConfig() RuleConfigSnapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.rulesConfigLocked()
}

func (e *Engine) rulesConfigLocked() RuleConfigSnapshot {
	s := e.ruleConfig
	out := RuleConfigSnapshot{Revision: s.revision(), Persistent: s.path != "", Rules: cloneRules(e.rules),
		Base: cloneRules(s.base), Removed: append([]string{}, s.state.Removed...), Overridden: []string{}, Invalid: append([]string{}, s.invalid...)}
	for _, spec := range s.state.Overrides {
		out.Overridden = append(out.Overridden, spec.Name)
	}
	for _, spec := range s.overlay.Rules {
		out.TemplateRules = append(out.TemplateRules, spec.Name)
	}
	out.Templates = append(out.Templates, s.overlay.Templates...)
	sort.Strings(out.Removed)
	sort.Strings(out.Overridden)
	sort.Strings(out.TemplateRules)
	sort.Strings(out.Templates)
	return out
}

// SetOverlay replaces the hub-pushed template overlay atomically: compile
// errors reject the whole overlay and the previous one stays. The new layer
// is persisted to template-overlay.json so it survives restarts that happen
// before the next hub push.
func (e *Engine) SetOverlay(o OverlayLayer) error {
	e.tickMu.Lock()
	defer e.tickMu.Unlock()
	e.mu.RLock()
	s := e.ruleConfig
	if e.closed {
		e.mu.RUnlock()
		return ErrRuleInvalid
	}
	macros := make(map[string]string, len(s.macros)+len(o.Macros))
	for k, v := range s.macros {
		macros[k] = v
	}
	for k, v := range o.Macros {
		macros[k] = v
	}
	e.mu.RUnlock()
	next := o
	rules, invalid, err := effectiveRules(s.base, s.state, macros, &next, true)
	if err != nil {
		return err
	}
	if s.overlayPath != "" {
		b, err := json.Marshal(overlayFile{Version: 1, Overlay: next})
		if err != nil {
			return err
		}
		if err := persistAtomic(s.overlayPath, b); err != nil {
			return err
		}
	}
	e.mu.Lock()
	s.overlay = next
	s.invalid = invalid
	current := make(map[string]*Rule, len(e.rules))
	for _, rule := range e.rules {
		current[rule.Spec.Name] = rule
	}
	changed := make(map[string]bool)
	for _, rule := range rules {
		// ExpandedHash covers the macro-resolved spec, so a macro-only
		// change refreshes only the rules that actually resolved differently.
		if previous := current[rule.Spec.Name]; previous == nil || previous.ExpandedHash() != rule.ExpandedHash() {
			changed[rule.Spec.Name] = true
		}
		delete(current, rule.Spec.Name)
	}
	for name := range current {
		changed[name] = true
	}
	e.rules = rules
	removedEntries := make([]struct {
		a     *Alarm
		entry LogEntry
	}, 0)
	now := e.now()
	for key, alarm := range e.alarms {
		if changed[alarm.Name] {
			if alarm.Status == StatusWarning || alarm.Status == StatusCritical {
				old, oldValue := alarm.Status, alarm.Value
				alarm.Status = StatusRemoved
				alarm.LastStatusChange = now.Unix()
				removedEntries = append(removedEntries, struct {
					a     *Alarm
					entry LogEntry
				}{alarm, e.newEntry(alarm, old, oldValue, now)})
			}
			delete(e.alarms, key)
		}
	}
	for key, group := range e.groups {
		kept := group.entries[:0]
		for _, entry := range group.entries {
			if !changed[entry.Name] {
				kept = append(kept, entry)
			}
		}
		group.entries = kept
		if len(kept) == 0 {
			delete(e.groups, key)
		}
	}
	e.mu.Unlock()
	for _, removed := range removedEntries {
		e.transition(removed.a, removed.entry, now)
	}
	return nil
}

type preparedRuleMutation struct {
	state   ruleConfigState
	rules   []*Rule
	changed map[string]bool
	encoded []byte
	invalid []string
}

// MutateRules validates and commits the complete batch or leaves both the live
// engine and the durable file unchanged. Nil expected preserves legacy writes.
func (e *Engine) MutateRules(expected *string, mutation RuleMutation) (RuleConfigSnapshot, error) {
	e.tickMu.Lock()
	defer e.tickMu.Unlock()
	prepared, err := e.prepareRuleMutation(expected, mutation)
	if err != nil {
		return RuleConfigSnapshot{}, err
	}
	if err := e.ruleConfig.persist(prepared.encoded); err != nil {
		return RuleConfigSnapshot{}, err
	}
	e.mu.Lock()
	e.ruleConfig.state = prepared.state
	e.ruleConfig.invalid = prepared.invalid
	e.rules = prepared.rules
	now := e.now()
	removedEntries := make([]struct {
		a     *Alarm
		entry LogEntry
	}, 0)
	for key, alarm := range e.alarms {
		if prepared.changed[alarm.Name] {
			if alarm.Status == StatusWarning || alarm.Status == StatusCritical {
				old, oldValue := alarm.Status, alarm.Value
				alarm.Status = StatusRemoved
				alarm.LastStatusChange = now.Unix()
				removedEntries = append(removedEntries, struct {
					a     *Alarm
					entry LogEntry
				}{alarm, e.newEntry(alarm, old, oldValue, now)})
			}
			delete(e.alarms, key)
		}
	}
	for key, group := range e.groups {
		kept := group.entries[:0]
		for _, entry := range group.entries {
			if !prepared.changed[entry.Name] {
				kept = append(kept, entry)
			}
		}
		group.entries = kept
		if len(kept) == 0 {
			delete(e.groups, key)
		}
	}
	out := e.rulesConfigLocked()
	e.mu.Unlock()
	for _, removed := range removedEntries {
		e.transition(removed.a, removed.entry, now)
	}
	return out, nil
}

// ValidateRuleMutation performs the same checks as commit, without writing a
// file, changing the revision, evaluating alarms, or dispatching notifications.
func (e *Engine) ValidateRuleMutation(expected *string, mutation RuleMutation) error {
	e.tickMu.Lock()
	defer e.tickMu.Unlock()
	_, err := e.prepareRuleMutation(expected, mutation)
	return err
}

// The caller holds tickMu; engine.mu remains available to snapshot readers
// throughout validation and disk I/O.
func (e *Engine) prepareRuleMutation(expected *string, mutation RuleMutation) (*preparedRuleMutation, error) {
	count := len(mutation.Upserts) + len(mutation.Delete) + len(mutation.Restore)
	if count == 0 || (mutation.CreateOnly && len(mutation.Delete)+len(mutation.Restore) != 0) {
		return nil, ErrRuleInvalid
	}
	if count > RuleOverrideLimit {
		return nil, ErrRuleCapacity
	}
	seen := make(map[string]bool, count)
	// Runtime rules compile against global + template-overlay macros, same as
	// the effective rule set does.
	e.mu.RLock()
	mergedMacros := make(map[string]string, len(e.ruleConfig.macros)+len(e.ruleConfig.overlay.Macros))
	for k, v := range e.ruleConfig.macros {
		mergedMacros[k] = v
	}
	for k, v := range e.ruleConfig.overlay.Macros {
		mergedMacros[k] = v
	}
	e.mu.RUnlock()
	for _, spec := range mutation.Upserts {
		if seen[spec.Name] {
			return nil, fmt.Errorf("%w: duplicate rule %q", ErrRuleInvalid, spec.Name)
		}
		seen[spec.Name] = true
		if _, err := CompileWith(spec, "api", mergedMacros); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrRuleInvalid, err)
		}
	}
	for _, names := range [][]string{mutation.Delete, mutation.Restore} {
		for _, name := range names {
			if strings.TrimSpace(name) == "" || seen[name] {
				return nil, fmt.Errorf("%w: duplicate or empty rule name", ErrRuleInvalid)
			}
			seen[name] = true
		}
	}
	e.mu.RLock()
	s := e.ruleConfig
	if e.closed || (expected != nil && *expected != s.revision()) {
		e.mu.RUnlock()
		return nil, ErrRuleConflict
	}
	current := make(map[string]*Rule, len(e.rules))
	for _, rule := range e.rules {
		current[rule.Spec.Name] = rule
	}
	e.mu.RUnlock()
	base := make(map[string]bool, len(s.base))
	for _, rule := range s.base {
		base[rule.Spec.Name] = true
	}
	overrides := make(map[string]RuleSpec, len(s.state.Overrides))
	for _, spec := range s.state.Overrides {
		overrides[spec.Name] = spec
	}
	removed := make(map[string]bool, len(s.state.Removed))
	for _, name := range s.state.Removed {
		removed[name] = true
	}
	for _, spec := range mutation.Upserts {
		if mutation.CreateOnly && current[spec.Name] != nil {
			return nil, fmt.Errorf("%w: %s", ErrRuleConflict, spec.Name)
		}
		overrides[spec.Name] = cloneRuleSpec(spec)
		delete(removed, spec.Name)
	}
	for _, name := range mutation.Delete {
		// An override invalid under the current overlay is not in the live
		// set but is still deletable (it exists in state.Overrides).
		if current[name] == nil {
			if _, isOverride := overrides[name]; !isOverride {
				return nil, fmt.Errorf("%w: %s", ErrRuleNotFound, name)
			}
		}
		delete(overrides, name)
		if base[name] {
			removed[name] = true
		}
	}
	for _, name := range mutation.Restore {
		// A configured rule may disappear after an upgrade. Its surviving
		// tombstone must still be removable, even though no rule is restored.
		if !base[name] && !removed[name] {
			return nil, fmt.Errorf("%w: no configured definition for %s", ErrRuleNotFound, name)
		}
		if _, overridden := overrides[name]; !overridden && !removed[name] {
			return nil, fmt.Errorf("%w: no override or deletion for %s", ErrRuleInvalid, name)
		}
		delete(overrides, name)
		delete(removed, name)
	}
	if s.state.Counter == ^uint64(0) {
		return nil, ErrRuleCapacity
	}
	next := ruleConfigState{Version: 1, Counter: s.state.Counter + 1, Overrides: []RuleSpec{}, Removed: []string{}}
	for _, spec := range overrides {
		next.Overrides = append(next.Overrides, spec)
	}
	for name := range removed {
		next.Removed = append(next.Removed, name)
	}
	sort.Slice(next.Overrides, func(i, j int) bool { return next.Overrides[i].Name < next.Overrides[j].Name })
	sort.Strings(next.Removed)
	rules, invalid, err := effectiveRules(s.base, next, s.effectiveMacros(), &s.overlay, true)
	if err != nil {
		return nil, err
	}
	// Only reject when the rule this mutation introduces fails to compile
	// now; pre-existing invalid overrides unrelated to the mutation are
	// tolerated (and remain listed in the store's invalid set).
	upsertNames := make(map[string]bool, len(mutation.Upserts))
	for _, spec := range mutation.Upserts {
		upsertNames[spec.Name] = true
	}
	for _, name := range invalid {
		if upsertNames[name] {
			return nil, fmt.Errorf("%w: rule %q does not compile under the current macros", ErrRuleInvalid, name)
		}
	}
	preparedInvalid := invalid
	b, err := json.Marshal(next)
	if err != nil {
		return nil, err
	}
	if len(b) > maxRuleConfigBytes {
		return nil, ErrRuleCapacity
	}
	changed := make(map[string]bool)
	for _, rule := range rules {
		name := rule.Spec.Name
		if previous := current[name]; previous == nil || previous.Hash() != rule.Hash() {
			changed[name] = true
		}
		delete(current, name)
	}
	for name := range current {
		changed[name] = true
	}
	return &preparedRuleMutation{state: next, rules: rules, changed: changed, encoded: b, invalid: preparedInvalid}, nil
}

func (s *ruleConfigStore) persist(b []byte) error {
	if s.path == "" {
		return nil
	}
	return persistAtomic(s.path, b)
}

func persistAtomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".persist-*") // owner-only
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
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("persist %s: %w", filepath.Base(path), err)
	}
	return nil
}

// MatchesRuleChart is shared by binding and read-only rule-scope previews.
func MatchesRuleChart(spec RuleSpec, chart *registry.Chart) bool {
	return chart != nil && (chart.ID == spec.On || chart.Context == spec.On) &&
		(len(spec.Labels) == 0 || labelsMatch(spec.Labels, chart.LabelsSnapshot()))
}
