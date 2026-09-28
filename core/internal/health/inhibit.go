package health

import (
	"fmt"
	"strings"
)

const (
	inhibitMaxRules = 32
	inhibitMaxNames = 32
	inhibitMaxEqual = 8
	inhibitNameMax  = 64
)

// InhibitRule keeps a target alarm visible while a source alarm is raised,
// and suppresses its warning and critical notifications. This is the local
// form of an Alertmanager inhibit rule, a Zabbix trigger dependency, and a
// Nagios notification dependency.
type InhibitRule struct {
	Source  string   `yaml:"source" json:"source"`
	Status  string   `yaml:"status,omitempty" json:"status,omitempty"` // critical (default) or warning
	Targets []string `yaml:"targets" json:"targets"`                   // alarm names, or ["*"]
	Equal   []string `yaml:"equal,omitempty" json:"equal,omitempty"`   // chart, context, family, class, type, component, label:<key>
}

type compiledInhibit struct {
	source  string
	warning bool
	all     bool
	targets map[string]struct{}
	equal   []inhibitKey
}

type inhibitKey struct {
	field string
	label string
}

func compileInhibit(rules []InhibitRule) ([]compiledInhibit, error) {
	if len(rules) > inhibitMaxRules {
		return nil, fmt.Errorf("health.inhibit: at most %d rules", inhibitMaxRules)
	}
	out := make([]compiledInhibit, 0, len(rules))
	for i, raw := range rules {
		compiled, err := compileOneInhibit(raw)
		if err != nil {
			return nil, fmt.Errorf("health.inhibit[%d]: %w", i, err)
		}
		out = append(out, compiled)
	}
	return out, nil
}

func compileOneInhibit(raw InhibitRule) (compiledInhibit, error) {
	source := strings.TrimSpace(raw.Source)
	if !validInhibitName(source) {
		return compiledInhibit{}, fmt.Errorf("source must be 1..%d letters, digits, '_', '-' or '.'", inhibitNameMax)
	}
	status := strings.ToLower(strings.TrimSpace(raw.Status))
	switch status {
	case "", "critical", "warning":
	default:
		return compiledInhibit{}, fmt.Errorf("status must be critical or warning")
	}
	if len(raw.Targets) == 0 || len(raw.Targets) > inhibitMaxNames {
		return compiledInhibit{}, fmt.Errorf("targets must list 1..%d alarm names", inhibitMaxNames)
	}
	compiled := compiledInhibit{
		source:  source,
		warning: status == "warning",
		targets: map[string]struct{}{},
	}
	seenTarget := map[string]struct{}{}
	for _, target := range raw.Targets {
		target = strings.TrimSpace(target)
		if target == "*" {
			if len(raw.Targets) != 1 {
				return compiledInhibit{}, fmt.Errorf("target * cannot be combined with alarm names")
			}
			compiled.all = true
			continue
		}
		if !validInhibitName(target) {
			return compiledInhibit{}, fmt.Errorf("target %q is not an alarm name", target)
		}
		if _, ok := seenTarget[target]; ok {
			return compiledInhibit{}, fmt.Errorf("duplicate target %q", target)
		}
		seenTarget[target] = struct{}{}
		compiled.targets[target] = struct{}{}
	}
	if len(raw.Equal) > inhibitMaxEqual {
		return compiledInhibit{}, fmt.Errorf("equal lists at most %d fields", inhibitMaxEqual)
	}
	seenEqual := map[string]struct{}{}
	for _, key := range raw.Equal {
		key = strings.TrimSpace(key)
		if _, ok := seenEqual[key]; ok {
			return compiledInhibit{}, fmt.Errorf("duplicate equal %q", key)
		}
		parsed, err := parseInhibitEqual(key)
		if err != nil {
			return compiledInhibit{}, err
		}
		seenEqual[key] = struct{}{}
		compiled.equal = append(compiled.equal, parsed)
	}
	return compiled, nil
}

func parseInhibitEqual(key string) (inhibitKey, error) {
	switch key {
	case "chart", "context", "family", "class", "type", "component":
		return inhibitKey{field: key}, nil
	}
	label, ok := strings.CutPrefix(key, "label:")
	if !ok || !validInhibitName(label) {
		return inhibitKey{}, fmt.Errorf("equal %q must be chart, context, family, class, type, component, or label:<key>", key)
	}
	return inhibitKey{label: label}, nil
}

func validInhibitName(name string) bool {
	if name == "" || len(name) > inhibitNameMax {
		return false
	}
	for i, r := range name {
		ok := r == '_' || r == '-' || r == '.' || (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
		if !ok || (i == 0 && (r == '.' || r == '-')) {
			return false
		}
	}
	return true
}

func (e *Engine) dependencyInhibitedLocked(entry LogEntry) bool {
	if entry.Status != StatusWarning && entry.Status != StatusCritical {
		return false
	}
	target := e.alarms[entry.Name+"|"+entry.Chart]
	for _, rule := range e.inhibit {
		if !rule.matchesName(entry.Name) {
			continue
		}
		for _, src := range e.alarms {
			if !rule.raised(src) || (src.Name == entry.Name && src.Chart == entry.Chart) {
				continue
			}
			if rule.same(src, target, entry) {
				return true
			}
		}
	}
	return false
}

func (r compiledInhibit) raised(a *Alarm) bool {
	if a == nil || a.Name != r.source {
		return false
	}
	if r.warning {
		return a.Status == StatusWarning || a.Status == StatusCritical
	}
	return a.Status == StatusCritical
}

func (r compiledInhibit) matchesName(name string) bool {
	if r.all {
		return name != r.source
	}
	_, ok := r.targets[name]
	return ok
}

func (r compiledInhibit) same(source *Alarm, target *Alarm, entry LogEntry) bool {
	for _, key := range r.equal {
		if inhibitSide(source, LogEntry{}, key) != inhibitSide(target, entry, key) || inhibitSide(source, LogEntry{}, key) == "" {
			return false
		}
	}
	return true
}

func inhibitSide(a *Alarm, entry LogEntry, key inhibitKey) string {
	if key.label != "" {
		if a == nil || a.Labels == nil {
			return ""
		}
		return a.Labels[key.label]
	}
	if a != nil {
		switch key.field {
		case "chart":
			return a.Chart
		case "context":
			return a.Context
		case "family":
			return a.Family
		case "class":
			return a.Class
		case "type":
			return a.Type
		case "component":
			return a.Component
		}
		return ""
	}
	switch key.field {
	case "chart":
		return entry.Chart
	case "context":
		return entry.Context
	case "family":
		return entry.Family
	case "class":
		return entry.Class
	case "type":
		return entry.Type
	case "component":
		return entry.Component
	}
	return ""
}
