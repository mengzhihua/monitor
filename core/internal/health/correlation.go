package health

import (
	"fmt"
	"strings"
)

// CorrelationRule marks symptom alarms while cause is raised. Symptom
// notifications are suppressed with reason "correlation"; the alarms stay visible.
// Equal lists fields that must match, the same keys as inhibit: chart, context,
// family, class, type, component, or label:<key>. Empty means any raised cause.
type CorrelationRule struct {
	Cause    string   `yaml:"cause" json:"cause"`
	Symptoms []string `yaml:"symptoms" json:"symptoms"`
	Equal    []string `yaml:"equal,omitempty" json:"equal,omitempty"`
}

type compiledCorrelation struct {
	cause    string
	symptoms map[string]bool
	equal    []inhibitKey
}

func compileCorrelation(rules []CorrelationRule) ([]compiledCorrelation, error) {
	if len(rules) > 32 {
		return nil, fmt.Errorf("at most 32 correlation rules")
	}
	out := make([]compiledCorrelation, 0, len(rules))
	for _, raw := range rules {
		if !validInhibitName(raw.Cause) {
			return nil, fmt.Errorf("correlation cause %q", raw.Cause)
		}
		if len(raw.Symptoms) == 0 || len(raw.Symptoms) > 32 {
			return nil, fmt.Errorf("correlation %s: 1..32 symptoms", raw.Cause)
		}
		compiled := compiledCorrelation{cause: raw.Cause, symptoms: map[string]bool{}}
		for _, name := range raw.Symptoms {
			if !validInhibitName(name) || name == raw.Cause || compiled.symptoms[name] {
				return nil, fmt.Errorf("correlation symptom %q", name)
			}
			compiled.symptoms[name] = true
		}
		if len(raw.Equal) > inhibitMaxEqual {
			return nil, fmt.Errorf("correlation %s: at most %d equal fields", raw.Cause, inhibitMaxEqual)
		}
		seenEqual := map[string]bool{}
		for _, key := range raw.Equal {
			key = strings.TrimSpace(key)
			if seenEqual[key] {
				return nil, fmt.Errorf("correlation %s: duplicate equal %q", raw.Cause, key)
			}
			parsed, err := parseInhibitEqual(key)
			if err != nil {
				return nil, fmt.Errorf("correlation %s: %w", raw.Cause, err)
			}
			seenEqual[key] = true
			compiled.equal = append(compiled.equal, parsed)
		}
		out = append(out, compiled)
	}
	return out, nil
}

func (e *Engine) correlatedCauseLocked(symptom *Alarm) string {
	if symptom == nil {
		return ""
	}
	for _, rule := range e.correlation {
		if !rule.symptoms[symptom.Name] {
			continue
		}
		for _, cause := range e.alarms {
			if cause.Name != rule.cause || (cause.Status != StatusWarning && cause.Status != StatusCritical) {
				continue
			}
			if correlationSame(rule.equal, cause, symptom) {
				return rule.cause
			}
		}
	}
	return ""
}

func correlationSame(equal []inhibitKey, cause, symptom *Alarm) bool {
	for _, key := range equal {
		if inhibitSide(cause, LogEntry{}, key) != inhibitSide(symptom, LogEntry{}, key) || inhibitSide(cause, LogEntry{}, key) == "" {
			return false
		}
	}
	return true
}

func (e *Engine) correlationSuppressedLocked(entry LogEntry) bool {
	if entry.Status != StatusWarning && entry.Status != StatusCritical {
		return false
	}
	symptom := e.alarms[entry.Name+"|"+entry.Chart]
	if symptom == nil {
		symptom = &Alarm{Name: entry.Name, Chart: entry.Chart, Context: entry.Context, Family: entry.Family, Class: entry.Class, Type: entry.Type, Component: entry.Component}
	}
	return e.correlatedCauseLocked(symptom) != ""
}
