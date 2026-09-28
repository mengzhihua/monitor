package health

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	Revision   string   `json:"revision"`
	Persistent bool     `json:"persistent"`
	Rules      []*Rule  `json:"rules"`
	Base       []*Rule  `json:"base"`
	Removed    []string `json:"removed"`
	Overridden []string `json:"overridden"`
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
	path  string
	epoch string
	base  []*Rule
	state ruleConfigState
}

func cloneRuleSpec(spec RuleSpec) RuleSpec {
	if spec.Labels != nil {
		labels := make(map[string]string, len(spec.Labels))
		for key, value := range spec.Labels {
			labels[key] = value
		}
		spec.Labels = labels
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

func openRuleConfig(dir string, base []*Rule) (*ruleConfigStore, []*Rule, error) {
	var epoch [16]byte
	if _, err := rand.Read(epoch[:]); err != nil {
		return nil, nil, err
	}
	for _, rule := range base {
		if rule == nil {
			return nil, nil, fmt.Errorf("%w: nil base rule", ErrRuleInvalid)
		}
	}
	s := &ruleConfigStore{epoch: hex.EncodeToString(epoch[:]), base: cloneRules(Merge(base)),
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
	rules, err := effectiveRules(s.base, s.state)
	if err != nil {
		return nil, nil, err
	}
	return s, rules, nil
}

func effectiveRules(base []*Rule, state ruleConfigState) ([]*Rule, error) {
	if len(state.Overrides)+len(state.Removed) > RuleOverrideLimit {
		return nil, ErrRuleCapacity
	}
	seen := make(map[string]bool)
	overrides := make([]*Rule, 0, len(state.Overrides))
	for _, spec := range state.Overrides {
		if seen[spec.Name] {
			return nil, fmt.Errorf("%w: duplicate rule %q", ErrRuleInvalid, spec.Name)
		}
		seen[spec.Name] = true
		rule, err := Compile(cloneRuleSpec(spec), "api")
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrRuleInvalid, err)
		}
		overrides = append(overrides, rule)
	}
	removed := make(map[string]bool, len(state.Removed))
	for _, name := range state.Removed {
		if strings.TrimSpace(name) == "" || seen[name] || removed[name] {
			return nil, fmt.Errorf("%w: duplicate or empty removed rule", ErrRuleInvalid)
		}
		removed[name] = true
	}
	sort.Slice(overrides, func(i, j int) bool { return overrides[i].Spec.Name < overrides[j].Spec.Name })
	merged := Merge(base, overrides)
	rules := make([]*Rule, 0, len(merged))
	for _, rule := range merged {
		if !removed[rule.Spec.Name] {
			rules = append(rules, rule)
		}
	}
	return rules, nil
}

func (s *ruleConfigStore) revision() string {
	return s.epoch + ":" + strconv.FormatUint(s.state.Counter, 10)
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
		Base: cloneRules(s.base), Removed: append([]string{}, s.state.Removed...), Overridden: []string{}}
	for _, spec := range s.state.Overrides {
		out.Overridden = append(out.Overridden, spec.Name)
	}
	sort.Strings(out.Removed)
	sort.Strings(out.Overridden)
	return out
}

type preparedRuleMutation struct {
	state   ruleConfigState
	rules   []*Rule
	changed map[string]bool
	encoded []byte
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
	e.rules = prepared.rules
	for key, alarm := range e.alarms {
		if prepared.changed[alarm.Name] {
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
	for _, spec := range mutation.Upserts {
		if seen[spec.Name] {
			return nil, fmt.Errorf("%w: duplicate rule %q", ErrRuleInvalid, spec.Name)
		}
		seen[spec.Name] = true
		if _, err := Compile(spec, "api"); err != nil {
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
		if current[name] == nil {
			return nil, fmt.Errorf("%w: %s", ErrRuleNotFound, name)
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
	rules, err := effectiveRules(s.base, next)
	if err != nil {
		return nil, err
	}
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
	return &preparedRuleMutation{state: next, rules: rules, changed: changed, encoded: b}, nil
}

func (s *ruleConfigStore) persist(b []byte) error {
	if s.path == "" {
		return nil
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".alert-rules-*") // owner-only
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
	if err := os.Rename(f.Name(), s.path); err != nil {
		return fmt.Errorf("persist alert rules: %w", err)
	}
	return nil
}

// MatchesRuleChart is shared by binding and read-only rule-scope previews.
func MatchesRuleChart(spec RuleSpec, chart *registry.Chart) bool {
	return chart != nil && (chart.ID == spec.On || chart.Context == spec.On) &&
		(len(spec.Labels) == 0 || labelsMatch(spec.Labels, chart.LabelsSnapshot()))
}
