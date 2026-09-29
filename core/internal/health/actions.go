package health

import (
	"fmt"
	"strings"
	"time"
)

const (
	maxActions = 32
	maxSteps   = 8
)

// Action is one escalation policy. Conditions are combined with AND.
// Steps run while the alarm stays raised; Delay is measured from the last
// status change. Recovery replaces the notify role when the alarm clears.
type Action struct {
	Name       string       `yaml:"name" json:"name"`
	Severities []string     `yaml:"severities,omitempty" json:"severities,omitempty"`
	Alarms     []string     `yaml:"alarms,omitempty" json:"alarms,omitempty"`
	Charts     []string     `yaml:"charts,omitempty" json:"charts,omitempty"`
	Steps      []ActionStep `yaml:"steps" json:"steps"`
	Recovery   *ActionStep  `yaml:"recovery,omitempty" json:"recovery,omitempty"`
}

// ActionStep notifies a role and optionally runs one whitelisted command once.
type ActionStep struct {
	Delay   string `yaml:"delay,omitempty" json:"delay,omitempty"`
	Role    string `yaml:"role,omitempty" json:"role,omitempty"`
	Command string `yaml:"command,omitempty" json:"command,omitempty"`
}

type compiledAction struct {
	name       string
	severities map[Status]bool
	alarms     []string
	charts     []string
	steps      []compiledStep
	recovery   *compiledStep
}

type compiledStep struct {
	delay   time.Duration
	role    string
	command string
}

func compileActions(actions []Action) ([]compiledAction, error) {
	if len(actions) > maxActions {
		return nil, fmt.Errorf("at most %d actions", maxActions)
	}
	out := make([]compiledAction, 0, len(actions))
	seen := map[string]bool{}
	for _, raw := range actions {
		name := strings.TrimSpace(raw.Name)
		if !validInhibitName(name) || seen[name] {
			return nil, fmt.Errorf("action name %q", raw.Name)
		}
		seen[name] = true
		compiled := compiledAction{name: name, alarms: raw.Alarms, charts: raw.Charts}
		if len(raw.Severities) > 0 {
			compiled.severities = map[Status]bool{}
			for _, s := range raw.Severities {
				st, err := parseActionStatus(s)
				if err != nil {
					return nil, err
				}
				compiled.severities[st] = true
			}
		}
		if len(raw.Steps) == 0 || len(raw.Steps) > maxSteps {
			return nil, fmt.Errorf("action %s: 1..%d steps", name, maxSteps)
		}
		var err error
		if compiled.steps, err = compileSteps(name, raw.Steps); err != nil {
			return nil, err
		}
		if raw.Recovery != nil {
			steps, err := compileSteps(name, []ActionStep{*raw.Recovery})
			if err != nil {
				return nil, err
			}
			compiled.recovery = &steps[0]
		}
		out = append(out, compiled)
	}
	return out, nil
}

func compileSteps(action string, steps []ActionStep) ([]compiledStep, error) {
	out := make([]compiledStep, 0, len(steps))
	var prev time.Duration
	for i, st := range steps {
		d, err := parseActionDelay(st.Delay)
		if err != nil {
			return nil, fmt.Errorf("action %s: %w", action, err)
		}
		if i > 0 && d < prev {
			return nil, fmt.Errorf("action %s: step delays must be non-decreasing", action)
		}
		prev = d
		role := strings.TrimSpace(st.Role)
		cmd := strings.TrimSpace(st.Command)
		if role == "" && cmd == "" {
			return nil, fmt.Errorf("action %s: step needs a role or command", action)
		}
		if cmd != "" && !validInhibitName(cmd) {
			return nil, fmt.Errorf("action %s: command %q", action, cmd)
		}
		out = append(out, compiledStep{delay: d, role: role, command: cmd})
	}
	return out, nil
}

func parseActionDelay(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 || d > 24*time.Hour {
		return 0, fmt.Errorf("delay %q", raw)
	}
	return d, nil
}

func parseActionStatus(raw string) (Status, error) {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "WARNING":
		return StatusWarning, nil
	case "CRITICAL":
		return StatusCritical, nil
	case "CLEAR":
		return StatusClear, nil
	default:
		return 0, fmt.Errorf("severity %q", raw)
	}
}

func (a compiledAction) matches(name, chart string, status Status) bool {
	if status == StatusClear {
		if a.recovery == nil {
			return false
		}
	} else if len(a.severities) > 0 && !a.severities[status] {
		return false
	}
	if !matchGlob(a.alarms, name) || !matchGlob(a.charts, chart) {
		return false
	}
	return true
}

func matchGlob(pats []string, value string) bool {
	if len(pats) == 0 {
		return true
	}
	for _, pat := range pats {
		if pat == value || (strings.HasSuffix(pat, "*") && strings.HasPrefix(value, strings.TrimSuffix(pat, "*"))) {
			return true
		}
	}
	return false
}

// selectStep returns the latest step whose delay has elapsed.
func selectStep(steps []compiledStep, elapsed time.Duration) (compiledStep, int, bool) {
	var chosen compiledStep
	idx := 0
	ok := false
	for i, st := range steps {
		if elapsed >= st.delay {
			chosen = st
			idx = i + 1
			ok = true
		}
	}
	return chosen, idx, ok
}

func (e *Engine) applyActionLocked(entry *LogEntry, at int64) {
	if len(e.actions) == 0 {
		return
	}
	var alarm *Alarm
	for _, a := range e.alarms {
		if a.Name == entry.Name && a.Chart == entry.Chart {
			alarm = a
			break
		}
	}
	for _, action := range e.actions {
		if entry == nil || !action.matches(entry.Name, entry.Chart, entry.Status) {
			continue
		}
		if entry.Status == StatusClear {
			if action.recovery == nil {
				return
			}
			if action.recovery.role != "" {
				entry.Recipient = action.recovery.role
			}
			e.fireCommandLocked(alarm, action.recovery.command, -1)
			return
		}
		since := entry.When
		if alarm != nil && alarm.LastStatusChange > 0 {
			since = alarm.LastStatusChange
		}
		elapsed := time.Duration(at-since) * time.Second
		if elapsed < 0 {
			elapsed = 0
		}
		step, idx, ok := selectStep(action.steps, elapsed)
		if !ok {
			return
		}
		if step.role != "" {
			entry.Recipient = step.role
		}
		e.fireCommandLocked(alarm, step.command, idx)
		return
	}
}

func (e *Engine) fireCommandLocked(alarm *Alarm, command string, idx int) {
	if command == "" || e.opt.RunCommand == nil || alarm == nil || alarm.actionFired == idx {
		return
	}
	alarm.actionFired = idx
	run := e.opt.RunCommand
	go run(command)
}
