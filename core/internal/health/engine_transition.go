package health

import (
	"errors"
	"strings"
	"time"
)

func (e *Engine) repeatAfter(a *Alarm) time.Duration {
	if a.Acknowledged {
		return 0
	}
	switch a.Status {
	case StatusWarning:
		return a.rule.Repeat.Warning
	case StatusCritical:
		return a.rule.Repeat.Critical
	}
	return 0
}

func (e *Engine) newEntry(a *Alarm, old Status, oldValue float64, now time.Time) LogEntry {
	return LogEntry{
		AlarmID: a.ID, When: now.Unix(), Hostname: e.opt.Hostname,
		Name: a.Name, Chart: a.Chart, Context: a.Context, Family: a.Family,
		Class: a.Class, Type: a.Type, Component: a.Component,
		Status: a.Status, OldStatus: old, Value: a.Value, OldValue: oldValue,
		Units: a.Units, Info: a.Info, Recipient: a.Recipient,
	}
}

// transition applies the notification delay: the log entry is written now,
// notification happens when the delay expires unless the alarm has meanwhile
// returned to the status notifiers last heard about (a.notifiedSt), in which
// case the whole flap is dropped.
func (e *Engine) transition(a *Alarm, entry LogEntry, now time.Time) {
	d := a.rule.Delay
	var delay time.Duration
	switch {
	case entry.Status > entry.OldStatus && entry.OldStatus >= StatusClear:
		delay = d.Up
	case entry.Status < entry.OldStatus && entry.OldStatus >= StatusClear:
		delay = d.Down
	}
	e.mu.Lock()
	if delay > 0 {
		if now.Sub(a.lastDelayAt) < d.Max {
			a.delayMult *= d.Multiplier
		} else {
			a.delayMult = 1
		}
		delay = time.Duration(float64(delay) * a.delayMult)
		if delay > d.Max {
			delay = d.Max
		}
		a.lastDelayAt = now
	}
	entry.Delay = int64(delay / time.Second)
	notifyNow := delay == 0
	if !notifyNow {
		a.DelayUpTo = now.Add(delay).Unix()
	} else {
		a.DelayUpTo = 0
	}
	e.mu.Unlock()

	// Transitions into UNINITIALIZED/UNDEFINED, a CLEAR when nothing raised
	// was ever reported, and a return to the last reported status are logged
	// but never notified; a rule that starts out raised is. A no-data gap
	// keeps notifiedSt so the eventual CLEAR (or re-raise) is judged against
	// what notifiers last heard.
	e.mu.Lock()
	noData := entry.Status <= StatusUndefined
	silent := noData ||
		(entry.Status == StatusClear && a.notifiedSt <= StatusClear) ||
		entry.Status == a.notifiedSt
	if silent {
		a.pending = nil
		a.DelayUpTo = 0
		if !noData {
			a.notifiedSt = entry.Status
		}
		e.mu.Unlock()
		e.record(entry, false)
		return
	}
	if notifyNow {
		a.pending = nil
		from := a.notifiedSt
		a.notifiedSt = entry.Status
		e.mu.Unlock()
		saved := e.record(entry, false)
		saved.OldStatus = from // notifiers see the change since the last report
		e.notifyAt(saved, now.Unix())
		return
	}
	e.mu.Unlock()
	saved := e.record(entry, false)
	e.mu.Lock()
	a.pending = &saved
	e.mu.Unlock()
}

// flushPending sends delayed notifications whose delay expired. If the alarm
// has since returned to its pre-transition status the notification is dropped
// (that is the point of the delay).
func (e *Engine) flushPending(now time.Time) {
	e.mu.Lock()
	var ready []LogEntry
	for _, a := range e.alarms {
		if a.pending == nil || now.Unix() < a.DelayUpTo {
			continue
		}
		p := *a.pending
		a.pending = nil
		a.DelayUpTo = 0
		if a.Status == a.notifiedSt {
			continue
		}
		p.Status, p.Value, p.OldStatus = a.Status, a.Value, a.notifiedSt
		a.notifiedSt = a.Status
		ready = append(ready, p)
	}
	e.mu.Unlock()
	for _, p := range ready {
		e.notifyAt(p, now.Unix())
	}
}

// SetAcknowledged pauses repeat notifications for one raised alarm until its
// status changes or acknowledgement is cleared. The evaluated severity stays.
func (e *Engine) SetAcknowledged(chart, name string, on bool, user, note string) error {
	if e == nil {
		return ErrAlarmNotFound
	}
	note = clipAckNote(note)
	e.mu.Lock()
	var alarm *Alarm
	for _, cand := range e.alarms {
		if cand.Active && cand.Chart == chart && cand.Name == name {
			alarm = cand
			break
		}
	}
	if alarm == nil {
		e.mu.Unlock()
		return ErrAlarmNotFound
	}
	if alarm.Status != StatusWarning && alarm.Status != StatusCritical {
		e.mu.Unlock()
		return ErrAlarmNotRaised
	}
	alarm.Acknowledged = on
	if on {
		alarm.AckUser = user
		alarm.AckNote = note
	} else {
		alarm.AckUser = ""
		alarm.AckNote = ""
	}
	entry := e.newEntry(alarm, alarm.Status, alarm.Value, e.now())
	entry.Acknowledged = on
	entry.User = user
	entry.Comment = note
	e.mu.Unlock()
	e.record(entry, false)
	if on {
		e.dispatchAckActions(entry)
	}
	return nil
}

func clipAckNote(note string) string {
	note = strings.TrimSpace(note)
	runes := []rune(note)
	if len(runes) > 256 {
		return string(runes[:256])
	}
	return note
}

var ErrAlarmNotFound = errors.New("alarm not found")
var ErrAlarmNotRaised = errors.New("alarm not raised")
var ErrManualCloseDisabled = errors.New("manual close is disabled for this alarm")

// CloseAlarm is Zabbix's "close problem": an operator forces a raised alarm back
// to CLEAR. The CLEAR transition goes through the normal path so notifiers
// hear it and repeat timers stop; the rule keeps evaluating and re-raises on
// the next evaluation while the condition still holds.
//
// tickMu serializes the close with a whole evaluation cycle (Tick holds it
// across bind+evaluate+flushPending), so no evaluate() can interleave between
// the status mutation and the transition. e.mu is still released before
// transition, which re-locks it itself.
func (e *Engine) CloseAlarm(alarmID uint64, user, comment string) error {
	e.tickMu.Lock()
	defer e.tickMu.Unlock()
	now := e.now()
	e.mu.Lock()
	var a *Alarm
	for _, cand := range e.alarms {
		if cand.ID == alarmID {
			a = cand
			break
		}
	}
	if a == nil {
		e.mu.Unlock()
		return ErrAlarmNotFound
	}
	if a.Status <= StatusClear {
		e.mu.Unlock()
		return ErrAlarmNotRaised
	}
	if a.rule != nil && !a.rule.AllowClose {
		e.mu.Unlock()
		return ErrManualCloseDisabled
	}
	old, oldValue := a.Status, a.Value
	a.Status = StatusClear
	a.LastStatusChange = now.Unix()
	a.LastUpdated = now.Unix()
	a.clearSustain()
	a.RecoveryHold = false
	a.pending = nil
	a.DelayUpTo = 0
	entry := e.newEntry(a, old, oldValue, now)
	entry.Manual, entry.User, entry.Comment = true, user, comment
	e.mu.Unlock()
	e.transition(a, entry, now)
	return nil
}
