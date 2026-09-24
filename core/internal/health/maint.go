package health

import (
	"strconv"
	"strings"
	"time"
)

// MaintenanceWindow is a recurring clock window (Netdata health.d calendar).
// Start/End are "HH:MM" in the engine's local timezone. Empty weekdays = every day.
type MaintenanceWindow struct {
	Start    string   `yaml:"start" json:"start"`
	End      string   `yaml:"end" json:"end"`
	Weekdays []string `yaml:"weekdays" json:"weekdays,omitempty"`
}

func (e *Engine) Enabled() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.enabled
}

func (e *Engine) SetEnabled(on bool) {
	e.mu.Lock()
	e.enabled = on
	e.mu.Unlock()
}

// SetMaintenanceUntil silences notifications until unix (0 = clear, <0 = seconds from now).
func (e *Engine) SetMaintenanceUntil(until int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if until < 0 {
		until = e.now().Unix() - until
	}
	e.maintUntil = until
}

func (e *Engine) InMaintenance() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.inMaintenanceLocked(e.now())
}

func (e *Engine) inMaintenanceLocked(now time.Time) bool {
	if e.maintUntil > 0 && now.Unix() < e.maintUntil {
		return true
	}
	for _, w := range e.windows {
		if w.covers(now) {
			return true
		}
	}
	return e.plans.Matches("", "", now.Unix()) // global plans only
}

// OnCallWindow sends notifications to To while the local clock is inside the window.
// The first matching window wins. Recipient "silent" is left unchanged, and a later
// escalation can still replace To.
type OnCallWindow struct {
	Start    string   `yaml:"start" json:"start"`
	End      string   `yaml:"end" json:"end"`
	Weekdays []string `yaml:"weekdays" json:"weekdays,omitempty"`
	To       string   `yaml:"to" json:"to"`
}

func (w OnCallWindow) covers(now time.Time) bool {
	return (MaintenanceWindow{Start: w.Start, End: w.End, Weekdays: w.Weekdays}).covers(now)
}

func (e *Engine) onCallRecipientLocked(now time.Time) string {
	for _, w := range e.opt.OnCall {
		to := strings.TrimSpace(w.To)
		if to != "" && w.covers(now) {
			return to
		}
	}
	return ""
}

func (w MaintenanceWindow) covers(now time.Time) bool {
	if w.Start == "" || w.End == "" {
		return false
	}
	if len(w.Weekdays) > 0 && !weekdayMatch(w.Weekdays, now.Weekday()) {
		return false
	}
	start := clockMinutes(w.Start)
	end := clockMinutes(w.End)
	cur := now.Hour()*60 + now.Minute()
	if start == end {
		return true
	}
	if start < end {
		return cur >= start && cur < end
	}
	return cur >= start || cur < end // overnight
}

func clockMinutes(s string) int {
	s = strings.TrimSpace(s)
	h, m := 0, 0
	if i := strings.IndexByte(s, ':'); i > 0 {
		h, _ = strconv.Atoi(s[:i])
		m, _ = strconv.Atoi(s[i+1:])
	} else {
		h, _ = strconv.Atoi(s)
	}
	if h < 0 {
		h = 0
	}
	if h > 23 {
		h = 23
	}
	if m < 0 {
		m = 0
	}
	if m > 59 {
		m = 59
	}
	return h*60 + m
}

func weekdayMatch(names []string, d time.Weekday) bool {
	want := strings.ToLower(d.String()[:3])
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if len(n) >= 3 && n[:3] == want {
			return true
		}
	}
	return false
}

// ManageInfo is GET /api/v1/manage/health.
func (e *Engine) ManageInfo() map[string]any {
	sil := e.SilenceInfo()
	e.mu.RLock()
	defer e.mu.RUnlock()
	return map[string]any{
		"enabled": e.enabled, "silent": e.silenceAll, "silence": sil,
		"maintenance": e.inMaintenanceLocked(e.now()), "maint_until": e.maintUntil, "windows": e.windows,
	}
}

// AlarmSummary aggregates raised alarms by status / class / type / component.
func (e *Engine) AlarmSummary() map[string]any {
	status := map[string]int{}
	classes := map[string]int{}
	types := map[string]int{}
	components := map[string]int{}
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, a := range e.alarms {
		if !a.Active {
			continue
		}
		st := strings.ToUpper(a.Status.String())
		status[st]++
		if a.Class != "" {
			classes[a.Class]++
		}
		if a.Type != "" {
			types[a.Type]++
		}
		if a.Component != "" {
			components[a.Component]++
		}
	}
	return map[string]any{"status": status, "classes": classes, "types": types, "components": components}
}
