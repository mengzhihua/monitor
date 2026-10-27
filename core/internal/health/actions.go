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
	Name       string            `yaml:"name" json:"name"`
	Severities []string          `yaml:"severities,omitempty" json:"severities,omitempty"`
	Alarms     []string          `yaml:"alarms,omitempty" json:"alarms,omitempty"`
	Charts     []string          `yaml:"charts,omitempty" json:"charts,omitempty"`
	Labels     map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	// Ack limits the action to acknowledged (yes) or unacknowledged (no) problems.
	// Empty matches either state and does not run when acknowledgement is set.
	Ack      string       `yaml:"ack,omitempty" json:"ack,omitempty"`
	Steps    []ActionStep `yaml:"steps" json:"steps"`
	Recovery *ActionStep  `yaml:"recovery,omitempty" json:"recovery,omitempty"`
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
	labels     map[string]string
	ack        ackMatch
	steps      []compiledStep
	recovery   *compiledStep
}

// ackMatch is how an action treats problem acknowledgement.
type ackMatch int

const (
	ackAny ackMatch = iota
	ackYes
	ackNo
)

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
		labels, err := actionLabels(name, raw.Labels)
		if err != nil {
			return nil, err
		}
		ack, err := parseAckMatch(raw.Ack)
		if err != nil {
			return nil, fmt.Errorf("action %s: %w", name, err)
		}
		compiled := compiledAction{name: name, alarms: raw.Alarms, charts: raw.Charts, labels: labels, ack: ack}
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

func parseAckMatch(raw string) (ackMatch, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return ackAny, nil
	case "yes":
		return ackYes, nil
	case "no":
		return ackNo, nil
	default:
		return 0, fmt.Errorf("ack %q must be yes or no", raw)
	}
}

func (a compiledAction) ackAllows(acknowledged bool) bool {
	switch a.ack {
	case ackYes:
		return acknowledged
	case ackNo:
		return !acknowledged
	default:
		return true
	}
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

func (a compiledAction) matches(name, chart string, status Status, labels map[string]string) bool {
	if status == StatusClear {
		if a.recovery == nil {
			return false
		}
	} else if len(a.severities) > 0 && !a.severities[status] {
		return false
	}
	if !matchGlob(a.alarms, name) || !matchGlob(a.charts, chart) || !matchLabels(a.labels, labels) {
		return false
	}
	return true
}

func actionLabels(name string, labels map[string]string) (map[string]string, error) {
	if len(labels) == 0 {
		return nil, nil
	}
	if len(labels) > 8 {
		return nil, fmt.Errorf("action %s: at most 8 labels", name)
	}
	out := make(map[string]string, len(labels))
	for key, value := range labels {
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if key == "" || len(key) > 64 || value == "" || len(value) > 128 {
			return nil, fmt.Errorf("action %s: label %q", name, key)
		}
		out[key] = value
	}
	return out, nil
}

func matchLabels(need, have map[string]string) bool {
	for key, pattern := range need {
		value, ok := have[key]
		if !ok || !matchGlob([]string{pattern}, value) {
			return false
		}
	}
	return true
}

func alarmLabels(alarm *Alarm) map[string]string {
	if alarm == nil {
		return nil
	}
	return alarm.Labels
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
	acked := alarm != nil && alarm.Acknowledged
	for _, action := range e.actions {
		if entry == nil || !action.matches(entry.Name, entry.Chart, entry.Status, alarmLabels(alarm)) || !action.ackAllows(acked) {
			continue
		}
		if entry.Status == StatusClear {
			if action.recovery == nil {
				return
			}
			if action.recovery.role != "" {
				entry.Recipient = action.recovery.role
			}
			e.fireCommandLocked(alarm, entry, action.recovery.command, -1)
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
		e.fireCommandLocked(alarm, entry, step.command, idx)
		return
	}
}

// dispatchAckActions runs actions that require acknowledgement. Other actions
// stay on the normal notification path, so acknowledging a problem does not
// page them again.
func (e *Engine) dispatchAckActions(entry LogEntry) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || len(e.actions) == 0 {
		return
	}
	if e.suppressionReasonLocked(entry.Chart, entry.Name) != "" {
		return
	}
	var alarm *Alarm
	for _, a := range e.alarms {
		if a.Name == entry.Name && a.Chart == entry.Chart {
			alarm = a
			break
		}
	}
	if alarm == nil || !alarm.Acknowledged {
		return
	}
	for _, action := range e.actions {
		if action.ack != ackYes || !action.matches(entry.Name, entry.Chart, entry.Status, alarmLabels(alarm)) {
			continue
		}
		step := action.steps[0]
		if step.role != "" && step.role != "silent" {
			entry.Recipient = step.role
			e.enqueueNotifyLocked(entry)
		}
		e.fireCommandLocked(alarm, &entry, step.command, 1)
		return
	}
}

func (e *Engine) fireCommandLocked(alarm *Alarm, entry *LogEntry, command string, idx int) {
	if command == "" || e.opt.RunCommand == nil || alarm == nil || alarm.actionFired == idx {
		return
	}
	alarm.actionFired = idx
	inv := actionInvocation(alarm, entry, command, e.opt.Hostname)
	run := e.opt.RunCommand
	go run(inv)
}

// noteInhibitedActionLocked records a command step that would have run if
// inhibit had not returned before applyActionLocked. Same-chart inhibit is the
// required path; dependency/correlation remain notify-only unless wired later.
func (e *Engine) noteInhibitedActionLocked(entry *LogEntry, at int64, reason string) {
	if e.opt.OnActionInhibited == nil || len(e.actions) == 0 || entry == nil || entry.Status == StatusClear {
		return
	}
	var alarm *Alarm
	for _, a := range e.alarms {
		if a.Name == entry.Name && a.Chart == entry.Chart {
			alarm = a
			break
		}
	}
	if alarm == nil {
		return
	}
	acked := alarm.Acknowledged
	for _, action := range e.actions {
		if !action.matches(entry.Name, entry.Chart, entry.Status, alarmLabels(alarm)) || !action.ackAllows(acked) {
			continue
		}
		since := entry.When
		if alarm.LastStatusChange > 0 {
			since = alarm.LastStatusChange
		}
		elapsed := time.Duration(at-since) * time.Second
		if elapsed < 0 {
			elapsed = 0
		}
		step, idx, ok := selectStep(action.steps, elapsed)
		if !ok || step.command == "" {
			return
		}
		if alarm.actionInhibitNoted == idx {
			return
		}
		alarm.actionInhibitNoted = idx
		inv := actionInvocation(alarm, entry, step.command, e.opt.Hostname)
		cb := e.opt.OnActionInhibited
		go cb(inv, reason)
		return
	}
}

func actionInvocation(alarm *Alarm, entry *LogEntry, command, hostname string) ActionCommand {
	inv := ActionCommand{
		Name:      command,
		AlarmID:   alarm.ID,
		AlarmName: alarm.Name,
		Chart:     alarm.Chart,
		Hostname:  hostname,
	}
	if entry != nil && entry.Status == StatusClear && entry.OldStatus > StatusClear {
		inv.Recovery = true
		inv.EpisodeStatus = entry.OldStatus
		inv.EpisodeSince = alarm.prevStatusChange
		if inv.EpisodeSince <= 0 {
			inv.EpisodeSince = entry.When
		}
		return inv
	}
	inv.EpisodeStatus = alarm.Status
	inv.EpisodeSince = alarm.LastStatusChange
	if entry != nil && inv.EpisodeStatus == 0 {
		inv.EpisodeStatus = entry.Status
	}
	return inv
}
