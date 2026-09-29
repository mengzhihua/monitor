package health

import "fmt"

// CorrelationRule marks symptom alarms while cause is raised. Symptom
// notifications are suppressed with reason "correlation"; the alarms stay visible.
type CorrelationRule struct {
	Cause    string   `yaml:"cause" json:"cause"`
	Symptoms []string `yaml:"symptoms" json:"symptoms"`
}

type compiledCorrelation struct {
	cause    string
	symptoms map[string]bool
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
		out = append(out, compiled)
	}
	return out, nil
}

func (e *Engine) correlatedCauseLocked(name string) string {
	for _, rule := range e.correlation {
		if !rule.symptoms[name] {
			continue
		}
		for _, a := range e.alarms {
			if a.Name == rule.cause && (a.Status == StatusWarning || a.Status == StatusCritical) {
				return rule.cause
			}
		}
	}
	return ""
}

func (e *Engine) correlationSuppressedLocked(entry LogEntry) bool {
	if entry.Status != StatusWarning && entry.Status != StatusCritical {
		return false
	}
	return e.correlatedCauseLocked(entry.Name) != ""
}
