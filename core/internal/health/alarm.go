package health

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// Status is an alarm state. Numeric values match Netdata so expressions such
// as `$status >= $WARNING` behave identically.
type Status int

const (
	StatusRemoved       Status = -2
	StatusUninitialized Status = -1
	StatusUndefined     Status = 0
	StatusClear         Status = 1
	StatusWarning       Status = 3
	StatusCritical      Status = 4
)

func (s Status) String() string {
	switch s {
	case StatusRemoved:
		return "REMOVED"
	case StatusUninitialized:
		return "UNINITIALIZED"
	case StatusUndefined:
		return "UNDEFINED"
	case StatusClear:
		return "CLEAR"
	case StatusWarning:
		return "WARNING"
	case StatusCritical:
		return "CRITICAL"
	}
	return fmt.Sprintf("STATUS(%d)", int(s))
}

func (s Status) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

func (s *Status) UnmarshalJSON(b []byte) error {
	var str string
	if err := json.Unmarshal(b, &str); err != nil {
		return err
	}
	for _, c := range []Status{StatusRemoved, StatusUninitialized, StatusUndefined, StatusClear, StatusWarning, StatusCritical} {
		if c.String() == str {
			*s = c
			return nil
		}
	}
	return fmt.Errorf("unknown status %q", str)
}

// Alarm is a rule bound to one chart.
type Alarm struct {
	ID        uint64            `json:"id"`
	Name      string            `json:"name"`
	Chart     string            `json:"chart"`
	Context   string            `json:"context"`
	Family    string            `json:"family"`
	Class     string            `json:"class,omitempty"`
	Type      string            `json:"type,omitempty"`
	Component string            `json:"component,omitempty"`
	Units     string            `json:"units"`
	Info      string            `json:"info"`
	URL       string            `json:"url,omitempty"`
	OpData    string            `json:"opdata,omitempty"`
	Lookup    string            `json:"lookup,omitempty"`
	Calc      string            `json:"calc,omitempty"`
	Warn      string            `json:"warn,omitempty"`
	Crit      string            `json:"crit,omitempty"`
	Every     int64             `json:"update_every"`
	Recipient string            `json:"recipient"`
	Source    string            `json:"source"`
	Labels    map[string]string `json:"chart_labels,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`

	Status           Status  `json:"status"`
	Value            float64 `json:"value"`
	LastUpdated      int64   `json:"last_updated"`
	LastStatusChange int64   `json:"last_status_change"`
	Active           bool    `json:"active"`
	Silenced         bool    `json:"silenced,omitempty"`
	SustainStatus    Status  `json:"pending_status,omitempty"` // raised status waiting on `for`
	SustainSince     int64   `json:"pending_since,omitempty"`
	PendingUntil     int64   `json:"pending_until,omitempty"` // unix seconds when `for` commits
	HoldUntil        int64   `json:"hold_until,omitempty"`    // keep_firing_for deadline, unix seconds
	RecoveryHold     bool    `json:"recovery_hold,omitempty"` // raised until the recovery expression is true
	Cause            string  `json:"correlated_cause,omitempty"`
	Acknowledged     bool    `json:"acknowledged,omitempty"`
	AckUser          string  `json:"ack_user,omitempty"`
	AckNote          string  `json:"ack_note,omitempty"`

	// notification pacing
	DelayUpTo    int64 `json:"delay_up_to_timestamp,omitempty"`
	LastNotified int64 `json:"last_notified,omitempty"`

	rule               *Rule
	chart              *registry.Chart
	nextRun            time.Time
	pending            *LogEntry // transition waiting for its delay to expire
	sustainStatus      Status
	sustainSince       time.Time
	holdUntil          time.Time
	notifiedSt         Status // status last reported to notifiers (or silently settled)
	delayMult          float64
	lastDelayAt        time.Time
	lastNotifyAttempt  int64 // scheduler time, independent of successful delivery
	actionFired        int   // last escalation step whose command already ran; -1 = recovery
	actionInhibitNoted int   // last step index recorded as inhibited (avoids repeat spam)
	prevStatusChange   int64 // LastStatusChange before the latest transition (recovery episode key)
}

// LogEntry records a status transition (or a repeat notification).
type LogEntry struct {
	UniqueID     uint64  `json:"unique_id"`
	AlarmID      uint64  `json:"alarm_id"`
	When         int64   `json:"when"`
	Updated      int64   `json:"updated,omitempty"` // last evaluation time; set on state snapshots
	Hostname     string  `json:"hostname"`
	Name         string  `json:"name"`
	Chart        string  `json:"chart"`
	Context      string  `json:"context"`
	Family       string  `json:"family"`
	Class        string  `json:"class,omitempty"`
	Type         string  `json:"type,omitempty"`
	Component    string  `json:"component,omitempty"`
	Status       Status  `json:"status"`
	OldStatus    Status  `json:"old_status"`
	Value        float64 `json:"value"`
	OldValue     float64 `json:"old_value"`
	Units        string  `json:"units"`
	Info         string  `json:"info"`
	URL          string  `json:"url,omitempty"`
	OpData       string  `json:"opdata,omitempty"`
	Recipient    string  `json:"recipient"`
	Delay        int64   `json:"delay"`
	Repeat       bool    `json:"repeat,omitempty"`
	Multiple     bool    `json:"multiple,omitempty"` // a new problem event while the trigger stayed raised
	Notified     bool    `json:"notified"`
	NotifiedAt   int64   `json:"notified_at,omitempty"`
	Manual       bool    `json:"manual,omitempty"` // operator-closed problem (Zabbix "close problem")
	Acknowledged bool    `json:"acknowledged,omitempty"`
	User         string  `json:"user,omitempty"` // who closed it
	Comment      string  `json:"comment,omitempty"`

	testChannel string // explicit admin test; never serialized or persisted as an alarm
}

// JSON encoding: NaN/Inf values (an alarm that has no data yet) become null.

func nullable(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

func fromNullable(p *float64) float64 {
	if p == nil {
		return math.NaN()
	}
	return *p
}

func (a Alarm) MarshalJSON() ([]byte, error) {
	type plain Alarm
	return json.Marshal(struct {
		plain
		Value *float64 `json:"value"`
	}{plain(a), nullable(a.Value)})
}

func (l LogEntry) MarshalJSON() ([]byte, error) {
	type plain LogEntry
	return json.Marshal(struct {
		plain
		Value    *float64 `json:"value"`
		OldValue *float64 `json:"old_value"`
	}{plain(l), nullable(l.Value), nullable(l.OldValue)})
}

func (l *LogEntry) UnmarshalJSON(b []byte) error {
	type plain LogEntry
	var aux struct {
		plain
		Value    *float64 `json:"value"`
		OldValue *float64 `json:"old_value"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	*l = LogEntry(aux.plain)
	l.Value, l.OldValue = fromNullable(aux.Value), fromNullable(aux.OldValue)
	return nil
}
