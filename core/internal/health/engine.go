package health

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
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
	Lookup    string            `json:"lookup,omitempty"`
	Calc      string            `json:"calc,omitempty"`
	Warn      string            `json:"warn,omitempty"`
	Crit      string            `json:"crit,omitempty"`
	Every     int64             `json:"update_every"`
	Recipient string            `json:"recipient"`
	Source    string            `json:"source"`
	Labels    map[string]string `json:"chart_labels,omitempty"`

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

	// notification pacing
	DelayUpTo    int64 `json:"delay_up_to_timestamp,omitempty"`
	LastNotified int64 `json:"last_notified,omitempty"`

	rule              *Rule
	chart             *registry.Chart
	nextRun           time.Time
	pending           *LogEntry // transition waiting for its delay to expire
	sustainStatus     Status
	sustainSince      time.Time
	holdUntil         time.Time
	notifiedSt        Status // status last reported to notifiers (or silently settled)
	delayMult         float64
	lastDelayAt       time.Time
	lastNotifyAttempt int64 // scheduler time, independent of successful delivery
}

// LogEntry records a status transition (or a repeat notification).
type LogEntry struct {
	UniqueID   uint64  `json:"unique_id"`
	AlarmID    uint64  `json:"alarm_id"`
	When       int64   `json:"when"`
	Updated    int64   `json:"updated,omitempty"` // last evaluation time; set on state snapshots
	Hostname   string  `json:"hostname"`
	Name       string  `json:"name"`
	Chart      string  `json:"chart"`
	Context    string  `json:"context"`
	Family     string  `json:"family"`
	Class      string  `json:"class,omitempty"`
	Type       string  `json:"type,omitempty"`
	Component  string  `json:"component,omitempty"`
	Status     Status  `json:"status"`
	OldStatus  Status  `json:"old_status"`
	Value      float64 `json:"value"`
	OldValue   float64 `json:"old_value"`
	Units      string  `json:"units"`
	Info       string  `json:"info"`
	Recipient  string  `json:"recipient"`
	Delay      int64   `json:"delay"`
	Repeat     bool    `json:"repeat,omitempty"`
	Notified   bool    `json:"notified"`
	NotifiedAt int64   `json:"notified_at,omitempty"`
	Manual     bool    `json:"manual,omitempty"` // operator-closed problem (Zabbix "close problem")
	User       string  `json:"user,omitempty"`   // who closed it
	Comment    string  `json:"comment,omitempty"`

	testChannel string // explicit admin test; never serialized or persisted as an alarm
}

// Notifier delivers alarm transitions somewhere (Slack, email, webhook...).
type Notifier interface {
	Name() string
	Notify(ctx context.Context, e LogEntry) error
}

// Options configures the engine.
type Options struct {
	Rules            []*Rule
	Hostname         string
	LogDir           string // alarm-log.jsonl lives here; empty = memory only
	LogKeep          int    // entries kept in memory (default 1000)
	Notifiers        []Notifier
	HostVars         map[string]float64  // e.g. cpus, ram_total (MiB); referenced as $cpus
	Roles            map[string][]string // recipient role -> notifier names; "" role = all
	Logger           *slog.Logger
	OnEvent          func(e LogEntry) // called on every transition (for the live WS)
	Now              func() time.Time
	SilenceAll       bool
	InhibitSameChart bool          // a critical alarm suppresses warnings on the same chart
	Inhibit          []InhibitRule // source alarms suppress target notifications
	GroupWait        time.Duration // hold same-chart notifications and send one
	EscalateAfter    time.Duration // critical repeats use EscalateTo after this long
	EscalateTo       string
	OnCall           []OnCallWindow // local clock windows that set the notify role; empty = off
	Enabled          *bool          // nil/true = evaluate; false = pause the engine
	Windows          []MaintenanceWindow
	// Anomaly supplies per-dimension 0–100 rates for lookup `anomaly-bit`.
	Anomaly AnomalySource
	// Macros are global {$NAME} user macros; rule-level `macros:` override them.
	Macros map[string]string
}

// AnomalySource is implemented by the ML collector (duck-typed; no import cycle).
type AnomalySource interface {
	Rate(chart, dim string) (float64, bool)
	RatesBetween(chart, dim string, after, before int64) []float64
}

// Engine evaluates rules and tracks alarms for one host.
type Engine struct {
	opt        Options
	reg        *registry.Registry
	db         *tsdb.Store
	log        *slog.Logger
	now        func() time.Time
	rules      []*Rule
	tickMu     sync.Mutex // serializes evaluations with complete rule-set mutations
	ruleConfig *ruleConfigStore

	mu      sync.RWMutex
	alarms  map[string]*Alarm // rule name|chart id
	entries []LogEntry
	nextID  uint64
	nextLog uint64
	logFile *os.File

	notifyCh    chan LogEntry
	notified    atomic.Int64
	dispatchWG  sync.WaitGroup
	startOnce   sync.Once
	closeOnce   sync.Once
	closed      bool                 // notifyCh closed; guarded by mu
	diagnostics NotificationSnapshot // guarded by mu; current process only
	plans       *MaintenancePlanStore

	lastNotificationTest time.Time // guarded by mu; global test rate limit

	// runtime silence (health.silent seeds silenceAll). until=0 means forever.
	silenceAll   bool
	silenceUntil int64
	silenced     map[string]int64 // chart.name or name → until unix
	enabled      bool
	maintUntil   int64
	windows      []MaintenanceWindow
	anomaly      AnomalySource
	groups       map[string]*notifyGroup
	inhibit      []compiledInhibit
}

type notifyGroup struct {
	ready   time.Time
	entries []LogEntry
}

// logUpdate is appended to alarm-log.jsonl when a previously written entry
// is delivered, so notification state survives restarts.
type logUpdate struct {
	Update     string `json:"update"`
	UniqueID   uint64 `json:"unique_id"`
	NotifiedAt int64  `json:"notified_at"`
}

// New builds an engine; call Run to start evaluating.
func New(reg *registry.Registry, db *tsdb.Store, opt Options) (*Engine, error) {
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	if opt.LogKeep <= 0 {
		opt.LogKeep = 1000
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	inhibit, err := compileInhibit(opt.Inhibit)
	if err != nil {
		return nil, err
	}
	e := &Engine{opt: opt, reg: reg, db: db, log: opt.Logger, now: opt.Now, rules: opt.Rules,
		alarms: map[string]*Alarm{}, nextID: 1, nextLog: 1, notifyCh: make(chan LogEntry, 256),
		silenceAll: opt.SilenceAll, silenced: map[string]int64{}, enabled: true, windows: opt.Windows,
		anomaly: opt.Anomaly, inhibit: inhibit}
	e.initNotificationDiagnostics()
	e.plans, err = openMaintenancePlans(opt.LogDir)
	if err != nil {
		return nil, fmt.Errorf("open maintenance plans: %w", err)
	}
	e.ruleConfig, e.rules, err = openRuleConfig(opt.LogDir, opt.Rules, opt.Macros)
	if err != nil {
		return nil, fmt.Errorf("open alert rules: %w", err)
	}
	if opt.Enabled != nil {
		e.enabled = *opt.Enabled
	}
	if opt.LogDir != "" {
		if err := os.MkdirAll(opt.LogDir, 0o755); err != nil {
			return nil, err
		}
		path := filepath.Join(opt.LogDir, "alarm-log.jsonl")
		if err := e.loadLog(path); err != nil {
			e.log.Warn("alarm log unreadable, starting fresh", "path", path, "err", err)
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
		if err != nil {
			return nil, err
		}
		e.logFile = f
	}
	return e, nil
}

func (e *Engine) loadLog(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var up logUpdate
		if err := json.Unmarshal(sc.Bytes(), &up); err == nil && up.Update == "notified" {
			for i := len(e.entries) - 1; i >= 0; i-- {
				if e.entries[i].UniqueID == up.UniqueID {
					e.entries[i].Notified, e.entries[i].NotifiedAt = true, up.NotifiedAt
					break
				}
			}
			continue
		}
		var le LogEntry
		if err := json.Unmarshal(sc.Bytes(), &le); err != nil {
			continue
		}
		e.entries = append(e.entries, le)
		if le.UniqueID >= e.nextLog {
			e.nextLog = le.UniqueID + 1
		}
		if le.AlarmID >= e.nextID {
			e.nextID = le.AlarmID + 1
		}
	}
	if len(e.entries) > e.opt.LogKeep {
		e.entries = e.entries[len(e.entries)-e.opt.LogKeep:]
	}
	return sc.Err()
}

// Rules returns a snapshot of the compiled rule set.
func (e *Engine) Rules() []*Rule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return cloneRules(e.rules)
}

// SetAnomaly installs the ML bit source used by lookup `anomaly-bit`.
func (e *Engine) SetAnomaly(src AnomalySource) {
	e.mu.Lock()
	e.anomaly = src
	e.mu.Unlock()
}

// UpsertRule replaces a rule of the same name or appends it, then drops bound
// alarm instances so Tick/bind recreates them against the new spec.
func (e *Engine) UpsertRule(r *Rule) {
	if r == nil {
		return
	}
	if _, err := e.MutateRules(nil, RuleMutation{Upserts: []RuleSpec{r.Spec}}); err != nil {
		e.log.Warn("alert rule upsert rejected", "name", r.Spec.Name, "err", err)
	}
}

// RemoveRule drops a rule and its bound alarms. Returns false if unknown.
func (e *Engine) RemoveRule(name string) bool {
	_, err := e.MutateRules(nil, RuleMutation{Delete: []string{name}})
	return err == nil
}

// Now returns the engine clock (overridable in tests via Options.Now).
func (e *Engine) Now() time.Time { return e.now() }

// SetOnEvent installs the transition callback; call before Run.
func (e *Engine) SetOnEvent(fn func(LogEntry)) { e.opt.OnEvent = fn }

// Run evaluates alarms until ctx is cancelled, then Closes the engine.
func (e *Engine) Run(ctx context.Context) {
	e.startDispatch()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			e.Close()
			return
		case now := <-t.C:
			e.Tick(now)
		}
	}
}

// startDispatch launches the notification dispatcher (once).
func (e *Engine) startDispatch() {
	e.startOnce.Do(func() {
		e.dispatchWG.Add(1)
		go func() {
			defer e.dispatchWG.Done()
			e.dispatch()
		}()
	})
}

// Close stops accepting notifications, waits for queued ones to be delivered
// and closes the alarm log. Safe to call more than once.
func (e *Engine) Close() {
	e.closeOnce.Do(func() {
		e.tickMu.Lock()
		e.mu.Lock()
		e.closed = true
		close(e.notifyCh)
		e.mu.Unlock()
		e.tickMu.Unlock()
		e.dispatchWG.Wait()
		e.mu.Lock()
		if e.logFile != nil {
			_ = e.logFile.Close()
			e.logFile = nil
		}
		e.mu.Unlock()
	})
}

// Tick binds rules to any new charts and evaluates alarms that are due.
func (e *Engine) Tick(now time.Time) {
	e.tickMu.Lock()
	defer e.tickMu.Unlock()
	e.mu.RLock()
	closed := e.closed
	e.mu.RUnlock()
	if closed {
		return
	}
	e.flushNotifyGroups(now)
	if !e.Enabled() {
		return
	}
	e.bind()
	e.mu.Lock()
	due := make([]*Alarm, 0)
	for _, a := range e.alarms {
		if a.Active && !now.Before(a.nextRun) {
			due = append(due, a)
		}
	}
	e.mu.Unlock()
	sort.Slice(due, func(i, j int) bool { return due[i].ID < due[j].ID })
	for _, a := range due {
		e.evaluate(a, now)
	}
	e.flushPending(now)
}

// bind creates alarm instances for charts matching rules (templates match by
// context, plain alarms by chart id) and deactivates alarms whose chart is gone.
func (e *Engine) bind() {
	charts := e.reg.Charts()
	byID := make(map[string]*registry.Chart, len(charts))
	for _, c := range charts {
		byID[c.ID] = c
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range e.rules {
		if r.Spec.Disabled {
			continue
		}
		for _, c := range charts {
			if !MatchesRuleChart(r.Spec, c) {
				continue
			}
			key := r.Spec.Name + "|" + c.ID
			if a, ok := e.alarms[key]; ok {
				a.Active = true
				continue
			}
			a := &Alarm{
				ID: e.nextID, Name: r.Spec.Name, Chart: c.ID, Context: c.Context, Family: c.Family,
				Class: r.Spec.Class, Type: r.Spec.Type, Component: r.Spec.Compon,
				Units: r.Units, Info: r.Info, Lookup: r.Spec.Lookup, Calc: r.Spec.Calc,
				Warn: r.Spec.Warn, Crit: r.Spec.Crit, Every: int64(r.Every / time.Second),
				Recipient: r.Spec.To, Source: r.Source, Labels: c.LabelsSnapshot(),
				Status: StatusUninitialized, Value: math.NaN(), Active: true,
				rule: r, chart: c, delayMult: 1, notifiedSt: StatusUninitialized,
			}
			if a.Units == "" {
				a.Units = c.Units
			}
			e.nextID++
			e.alarms[key] = a
		}
	}
	for _, a := range e.alarms {
		if _, ok := byID[a.Chart]; !ok && a.Active {
			a.Active = false
		}
	}
}

func labelsMatch(want map[string]string, have map[string]string) bool {
	for k, v := range want {
		hv, ok := have[k]
		if !ok {
			return false
		}
		if v == "*" || v == hv {
			continue
		}
		if strings.HasSuffix(v, "*") && strings.HasPrefix(hv, strings.TrimSuffix(v, "*")) {
			continue
		}
		return false
	}
	return true
}

// evaluate runs one alarm: lookup → calc → warn/crit → transition.
func (e *Engine) evaluate(a *Alarm, now time.Time) {
	r := a.rule
	value := math.NaN()
	if r.Lookup != nil {
		value = e.lookup(a.chart, r.Lookup, now)
	}
	lastT, last := a.chart.LastValues()
	vars := e.variables(a, value, lastT, last, now)
	if r.Calc != nil {
		value = r.Calc.Eval(vars)
		vars = e.variables(a, value, lastT, last, now)
	}

	var status Status
	switch {
	case math.IsNaN(value) || math.IsInf(value, 0):
		status = StatusUndefined
	case r.Crit != nil && truthy(r.Crit.Eval(vars)):
		status = StatusCritical
	case r.Warn != nil && truthy(r.Warn.Eval(vars)):
		status = StatusWarning
	default:
		status = StatusClear
	}
	recovered := true
	if r.Recovery != nil {
		recovered = truthy(r.Recovery.Eval(vars))
	}

	e.mu.Lock()
	a.nextRun = now.Add(r.Every)
	old, oldValue := a.Status, a.Value
	a.Value = value
	a.LastUpdated = now.Unix()
	status = gateRecovery(a, status, recovered)
	status = gateRaised(a, status, now)
	if status != old {
		a.Status = status
		a.LastStatusChange = now.Unix()
		entry := e.newEntry(a, old, oldValue, now)
		e.mu.Unlock()
		e.transition(a, entry, now)
		return
	}
	// repeat notifications while raised
	if rep := e.repeatAfter(a); rep > 0 && a.pending == nil && a.Status == a.notifiedSt && a.lastNotifyAttempt > 0 && now.Sub(time.Unix(a.lastNotifyAttempt, 0)) >= rep {
		entry := e.newEntry(a, old, oldValue, now)
		entry.Repeat = true
		e.mu.Unlock()
		e.record(entry, true)
		return
	}
	e.mu.Unlock()
}

func (e *Engine) repeatAfter(a *Alarm) time.Duration {
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

var ErrAlarmNotFound = errors.New("alarm not found")
var ErrAlarmNotRaised = errors.New("alarm not raised")

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
	now := e.now()
	e.tickMu.Lock()
	defer e.tickMu.Unlock()
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

// record appends to the alarm log, fires OnEvent and optionally notifies.
func (e *Engine) record(entry LogEntry, notify bool) LogEntry {
	e.mu.Lock()
	entry.UniqueID = e.nextLog
	e.nextLog++
	e.entries = append(e.entries, entry)
	if len(e.entries) > e.opt.LogKeep {
		e.entries = e.entries[len(e.entries)-e.opt.LogKeep:]
	}
	if e.logFile != nil {
		if b, err := json.Marshal(entry); err == nil {
			_, _ = e.logFile.Write(append(b, '\n'))
		}
	}
	e.mu.Unlock()
	e.log.Info("alarm", "name", entry.Name, "chart", entry.Chart, "status", entry.Status.String(),
		"old", entry.OldStatus.String(), "value", entry.Value, "units", entry.Units, "delay", entry.Delay)
	if e.opt.OnEvent != nil {
		e.opt.OnEvent(entry)
	}
	if notify {
		e.notifyAt(entry, entry.When)
	}
	return entry
}

// notify queues an entry for delivery. Delivery state (Notified, NotifiedAt,
// Alarm.LastNotified) is only recorded once a notifier actually succeeds.
func (e *Engine) notify(entry LogEntry) {
	e.notifyAt(entry, e.now().Unix())
}

func (e *Engine) notifyAt(entry LogEntry, at int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	// Repeat cadence is about scheduled attempts. Suppression, failed delivery
	// and a full queue must neither stop all future repeats nor retry every tick.
	if a := e.alarms[entry.Name+"|"+entry.Chart]; a != nil && a.ID == entry.AlarmID && at > a.lastNotifyAttempt {
		a.lastNotifyAttempt = at
	}
	if reason := e.suppressionReasonLocked(entry.Chart, entry.Name); reason != "" {
		e.diagnostics.Suppressed++
		e.addNotificationResultLocked(entry, "", "suppressed", reason, 0, 0)
		return
	}
	if strings.TrimSpace(entry.Recipient) != "silent" {
		if to := e.onCallRecipientLocked(time.Unix(at, 0)); to != "" {
			entry.Recipient = to
		}
	}
	if e.opt.EscalateAfter > 0 && e.opt.EscalateTo != "" && entry.Repeat && entry.Status == StatusCritical {
		if a := e.alarms[entry.Name+"|"+entry.Chart]; a != nil && at-a.LastStatusChange >= int64(e.opt.EscalateAfter/time.Second) {
			entry.Recipient = e.opt.EscalateTo
		}
	}
	if e.opt.InhibitSameChart && entry.Status == StatusWarning {
		for _, a := range e.alarms {
			if a.Chart == entry.Chart && a.Name != entry.Name && a.Status == StatusCritical {
				e.diagnostics.Suppressed++
				e.addNotificationResultLocked(entry, "", "suppressed", "inhibited", 0, 0)
				return
			}
		}
	}
	if e.dependencyInhibitedLocked(entry) {
		e.diagnostics.Suppressed++
		e.addNotificationResultLocked(entry, "", "suppressed", "dependency", 0, 0)
		return
	}
	if strings.TrimSpace(entry.Recipient) == "silent" {
		e.diagnostics.Suppressed++
		e.addNotificationResultLocked(entry, "", "suppressed", "silent_recipient", 0, 0)
		return
	}
	if e.opt.GroupWait > 0 && entry.testChannel == "" {
		if e.groups == nil {
			e.groups = map[string]*notifyGroup{}
		}
		key := entry.Chart + "\n" + strings.TrimSpace(entry.Recipient)
		if g := e.groups[key]; g != nil {
			g.entries = append(g.entries, entry)
			e.diagnostics.Suppressed++
			e.addNotificationResultLocked(entry, "", "suppressed", "grouped", 0, 0)
			return
		}
		e.groups[key] = &notifyGroup{ready: time.Unix(at, 0).Add(e.opt.GroupWait), entries: []LogEntry{entry}}
		return
	}
	e.enqueueNotifyLocked(entry)
}

func (e *Engine) enqueueNotifyLocked(entry LogEntry) {
	if len(e.notifiersFor(entry.Recipient)) == 0 {
		e.diagnostics.Unrouted++
		e.addNotificationResultLocked(entry, "", "unrouted", "no_channel", 0, 0)
		return
	}
	select {
	case e.notifyCh <- entry:
		e.diagnostics.Enqueued++
	default:
		e.diagnostics.Dropped++
		e.addNotificationResultLocked(entry, "", "dropped", "queue_full", 0, 0)
		e.log.Warn("notification queue full, dropping", "alarm", entry.Name)
	}
}

func (e *Engine) flushNotifyGroups(now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for key, g := range e.groups {
		if now.Before(g.ready) || len(g.entries) == 0 {
			continue
		}
		first := g.entries[0]
		if len(g.entries) > 1 {
			names := make([]string, len(g.entries))
			for i, en := range g.entries {
				names[i] = en.Name
			}
			first.Info = strings.TrimSpace(first.Info + " also " + strings.Join(names, ", "))
		}
		delete(e.groups, key)
		e.enqueueNotifyLocked(first)
	}
}

func (e *Engine) dispatch() {
	for entry := range e.notifyCh {
		notifiers := e.notifiersFor(entry.Recipient)
		if entry.testChannel != "" {
			notifiers = nil
			for _, n := range e.opt.Notifiers {
				if diagnosticChannel(n.Name()) == entry.testChannel {
					notifiers = append(notifiers, n)
				}
			}
		}
		for _, n := range notifiers {
			channel := diagnosticChannel(n.Name())
			e.beginNotification(entry, channel)
			started := time.Now()
			cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := n.Notify(cctx, entry)
			cancel()
			code, httpStatus := notificationError(err)
			e.finishNotification(entry, channel, code, httpStatus, time.Since(started).Milliseconds())
			if err != nil {
				// Provider errors may contain webhook credentials or response bodies.
				e.log.Error("notify failed", "via", channel, "alarm", entry.Name, "reason", code, "http_status", httpStatus)
				continue
			}
			if entry.testChannel == "" {
				e.markNotified(entry)
				e.notified.Add(1)
			}
		}
	}
}

func (e *Engine) markNotified(entry LogEntry) {
	at := e.now().Unix()
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, a := range e.alarms {
		if a.ID == entry.AlarmID && entry.When > a.LastNotified {
			a.LastNotified = entry.When
		}
	}
	for i := range e.entries {
		if e.entries[i].UniqueID == entry.UniqueID {
			e.entries[i].Notified, e.entries[i].NotifiedAt = true, at
		}
	}
	if e.logFile != nil {
		if b, err := json.Marshal(logUpdate{Update: "notified", UniqueID: entry.UniqueID, NotifiedAt: at}); err == nil {
			_, _ = e.logFile.Write(append(b, '\n'))
		}
	}
}

func (e *Engine) RoutingInfo() map[string]any {
	e.mu.RLock()
	defer e.mu.RUnlock()
	channels := make([]map[string]any, 0, len(e.opt.Notifiers))
	for _, n := range e.opt.Notifiers {
		channels = append(channels, map[string]any{"name": n.Name(), "configured": true})
	}
	roles := e.opt.Roles
	if roles == nil {
		roles = map[string][]string{}
	}
	return map[string]any{"roles": roles, "channels": channels}
}

func (e *Engine) notifiersFor(role string) []Notifier {
	role = strings.TrimSpace(role)
	if role == "silent" {
		return nil
	}
	names, ok := e.opt.Roles[role]
	if !ok || len(names) == 0 {
		return e.opt.Notifiers
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var out []Notifier
	for _, n := range e.opt.Notifiers {
		if want[n.Name()] {
			out = append(out, n)
		}
	}
	return out
}

// Notified returns how many notifications were delivered successfully.
func (e *Engine) Notified() int64 { return e.notified.Load() }

// lookup runs the alarm's database query and reduces it to one number.
func (e *Engine) lookup(c *registry.Chart, l *Lookup, now time.Time) float64 {
	before := now.Unix()
	after := before - int64(l.After/time.Second)
	dims := c.Dims()
	selected := map[string]bool{}
	for _, d := range l.Dimensions {
		selected[d] = true
	}
	if l.Kind != "" {
		return e.lookupKind(c, l, selected, after, before)
	}
	var sum, total float64
	var matched bool
	var minSum, maxSum float64
	e.mu.RLock()
	src := e.anomaly
	e.mu.RUnlock()
	for _, d := range dims {
		var vals []float64
		if l.AnomalyBit {
			if src != nil {
				vals = src.RatesBetween(c.ID, d.ID, after, before)
			}
		} else {
			pts, err := e.db.Query(registry.SeriesID(c.ID, d.ID), after, before)
			if err != nil || len(pts) == 0 {
				continue
			}
			vals = make([]float64, 0, len(pts))
			for _, p := range pts {
				if !math.IsNaN(p.Value) {
					vals = append(vals, p.Value)
				}
			}
		}
		if len(vals) == 0 {
			continue
		}
		var v float64
		if !l.AnomalyBit && l.Method == tsdb.GroupSum {
			pts, err := e.db.Query(registry.SeriesID(c.ID, d.ID), after, before)
			if err != nil {
				continue
			}
			v = integrate(pts, int64(c.UpdateEvery))
		} else {
			v = reduce(vals, l.Method)
		}
		if l.AbsValue {
			v = math.Abs(v)
		}
		isSel := len(selected) == 0 || selected[d.ID] || selected[d.Name]
		if isSel {
			matched = true
			sum += v
			if l.MinMax != "" {
				minSum += reduce(vals, tsdb.GroupMin)
				maxSum += reduce(vals, tsdb.GroupMax)
			}
		}
		total += v
	}
	if !matched {
		return math.NaN()
	}
	if l.MinMax != "" {
		return maxSum - minSum
	}
	if l.Percentage {
		if total == 0 {
			return math.NaN()
		}
		return sum * 100 / total
	}
	return sum
}

// lookupKind evaluates the Zabbix-style functions (Lookup.Kind). All of them
// work on [after, before] over the selected dimensions; per-dimension results
// are summed unless noted.
func (e *Engine) lookupKind(c *registry.Chart, l *Lookup, selected map[string]bool, after, before int64) float64 {
	isSel := func(d *registry.Dimension) bool {
		return len(selected) == 0 || selected[d.ID] || selected[d.Name]
	}
	if l.Kind == "nodata" {
		// 1 when no non-NaN point exists for any selected dim; never NaN.
		for _, d := range c.Dims() {
			if !isSel(d) {
				continue
			}
			pts, err := e.db.Query(registry.SeriesID(c.ID, d.ID), after, before)
			if err != nil {
				continue
			}
			for _, p := range pts {
				if !math.IsNaN(p.Value) {
					return 0
				}
			}
		}
		return 1
	}
	if l.Kind == "trendavg" || l.Kind == "trendmin" || l.Kind == "trendmax" ||
		l.Kind == "trendsum" || l.Kind == "trendcount" {
		return e.lookupTrend(c, l, selected, after, before)
	}
	var sum float64
	matched := false
	if l.Kind == "timeleft" {
		sum = timeLeftSentinel
	}
	for _, d := range c.Dims() {
		if !isSel(d) {
			continue
		}
		pts, err := e.db.Query(registry.SeriesID(c.ID, d.ID), after, before)
		if err != nil {
			continue
		}
		if l.Kind == "count" {
			// count answers 0 for a selected dimension that simply has no
			// matching samples; only query failures keep it out.
			matched = true
		}
		vals := make([]tsdb.Point, 0, len(pts))
		for _, p := range pts {
			if !math.IsNaN(p.Value) {
				vals = append(vals, p)
			}
		}
		if len(vals) == 0 {
			continue
		}
		matched = true
		switch l.Kind {
		case "first":
			sum += vals[0].Value
		case "change":
			sum += vals[len(vals)-1].Value - vals[0].Value
		case "stddev":
			sum += popStddev(vals)
		case "count":
			sum += float64(countMatching(vals, l.CountOp, l.CountVal))
		case "forecast":
			a, b, ok := linearFit(vals)
			if !ok {
				continue
			}
			// fit origin is the first sample's timestamp
			x := float64(before-vals[0].TS) + l.Horizon.Seconds()
			sum += a + b*x
		case "timeleft":
			a, b, ok := linearFit(vals)
			if !ok || math.Abs(b) < 1e-12 {
				continue // stays at the sentinel
			}
			rem := (l.Target - (a + b*float64(before-vals[0].TS))) / b
			if rem < 0 {
				continue // moving away from the target: stays at the sentinel
			}
			if rem < sum {
				sum = rem
			}
		}
	}
	if !matched {
		return math.NaN()
	}
	if l.AbsValue {
		return math.Abs(sum)
	}
	return sum
}

// timeLeftSentinel replaces the unbounded Zabbix "never" answer; evaluate()
// treats +Inf as undefined, so timeleft reports a very large finite value.
const timeLeftSentinel = 1e15

func countMatching(pts []tsdb.Point, op string, v float64) int {
	var n int
	for _, p := range pts {
		ok := true
		switch op {
		case "gt":
			ok = p.Value > v
		case "ge":
			ok = p.Value >= v
		case "lt":
			ok = p.Value < v
		case "le":
			ok = p.Value <= v
		case "eq":
			ok = p.Value == v
		case "ne":
			ok = p.Value != v
		}
		if ok {
			n++
		}
	}
	return n
}

func popStddev(pts []tsdb.Point) float64 {
	var m float64
	for _, p := range pts {
		m += p.Value
	}
	m /= float64(len(pts))
	var v float64
	for _, p := range pts {
		d := p.Value - m
		v += d * d
	}
	return math.Sqrt(v / float64(len(pts)))
}

// linearFit is ordinary least squares of (seconds since window start, value).
func linearFit(pts []tsdb.Point) (a, b float64, ok bool) {
	if len(pts) < 2 {
		return 0, 0, false
	}
	x0 := float64(pts[0].TS)
	var sx, sy, sxx, sxy float64
	for _, p := range pts {
		x := float64(p.TS) - x0
		sx += x
		sy += p.Value
		sxx += x * x
		sxy += x * p.Value
	}
	n := float64(len(pts))
	den := n*sxx - sx*sx
	if math.Abs(den) < 1e-12 {
		return 0, 0, false
	}
	b = (n*sxy - sx*sy) / den
	a = (sy - b*sx) / n
	return a, b, true
}

// lookupTrend reduces rollup-tier buckets; empty coverage falls back to the
// raw tier-0 samples so fresh installs still answer.
func (e *Engine) lookupTrend(c *registry.Chart, l *Lookup, selected map[string]bool, after, before int64) float64 {
	var sum float64
	matched := false
	for _, d := range c.Dims() {
		if len(selected) != 0 && !selected[d.ID] && !selected[d.Name] {
			continue
		}
		id := registry.SeriesID(c.ID, d.ID)
		// Pick the finest tier that still reaches back to `after`; when the
		// window is too old for tier0 (retention) or a tier was enabled late,
		// fall back to the coarsest tier that has any data. Tier0 buckets are
		// single samples, so the reduction below is uniform.
		nTiers := len(e.db.Tiers())
		var bs []tsdb.Bucket
		for tier := 0; tier < nTiers; tier++ {
			if !e.db.TierCovers(id, tier, after) {
				continue
			}
			b, err := e.db.QueryTier(id, tier, after, before)
			if err == nil && len(b) > 0 {
				bs = b
				break
			}
		}
		if len(bs) == 0 {
			for tier := nTiers - 1; tier >= 0; tier-- {
				b, err := e.db.QueryTier(id, tier, after, before)
				if err == nil && len(b) > 0 {
					bs = b
					break
				}
			}
		}
		if len(bs) == 0 {
			continue
		}
		var v float64
		{
			switch l.Kind {
			case "trendavg":
				var s float64
				var n int64
				for _, b := range bs {
					s += b.Sum
					n += b.Count
				}
				if n == 0 {
					continue
				}
				v = s / float64(n)
			case "trendmin":
				v = bs[0].Min
				for _, b := range bs[1:] {
					if b.Min < v {
						v = b.Min
					}
				}
			case "trendmax":
				v = bs[0].Max
				for _, b := range bs[1:] {
					if b.Max > v {
						v = b.Max
					}
				}
			case "trendsum":
				for _, b := range bs {
					v += b.Sum
				}
			case "trendcount":
				for _, b := range bs {
					v += float64(b.Count)
				}
			}
		}
		matched = true
		sum += v
	}
	if !matched {
		return math.NaN()
	}
	return sum
}

// integrate sums per-second rates over the time they were in effect, so
// `sum` yields a total regardless of the collection interval. Each sample
// covers the gap since the previous one, capped at twice the chart interval
// (a longer gap means missed collections, not a sustained rate).
func integrate(pts []tsdb.Point, every int64) float64 {
	if every <= 0 {
		every = 1
	}
	var total float64
	prev := int64(-1)
	for _, p := range pts {
		dt := every
		if prev >= 0 {
			if d := p.TS - prev; d > 0 && d <= 2*every {
				dt = d
			}
		}
		prev = p.TS
		if !math.IsNaN(p.Value) {
			total += p.Value * float64(dt)
		}
	}
	return total
}

func reduce(vals []float64, fn tsdb.GroupFunc) float64 {
	res := tsdb.Aggregate([][]tsdb.Point{pointsOf(vals)}, 0, int64(len(vals)), 1, fn)
	if len(res.Values) == 0 || len(res.Values[0]) == 0 {
		return math.NaN()
	}
	return res.Values[0][0]
}

func pointsOf(vals []float64) []tsdb.Point {
	pts := make([]tsdb.Point, len(vals))
	for i, v := range vals {
		pts[i] = tsdb.Point{TS: int64(i + 1), Value: v}
	}
	return pts
}

// variables resolves $names for expressions: alarm constants, $this,
// $status, the chart's latest dimension values, and other alarms of the chart.
func (e *Engine) variables(a *Alarm, this float64, lastT int64, last map[string]float64, now time.Time) func(string) (float64, bool) {
	return func(name string) (float64, bool) {
		switch name {
		case "this":
			return this, true
		case "status":
			return float64(a.Status), true
		case "REMOVED":
			return float64(StatusRemoved), true
		case "UNINITIALIZED":
			return float64(StatusUninitialized), true
		case "UNDEFINED":
			return float64(StatusUndefined), true
		case "CLEAR":
			return float64(StatusClear), true
		case "WARNING":
			return float64(StatusWarning), true
		case "CRITICAL":
			return float64(StatusCritical), true
		case "now":
			return float64(now.Unix()), true
		case "update_every":
			return float64(a.chart.UpdateEvery), true
		case "last_collected_t":
			return float64(lastT), true
		}
		if v, ok := last[name]; ok {
			return v, true
		}
		if v, ok := e.opt.HostVars[name]; ok {
			return v, true
		}
		for _, d := range a.chart.Dims() {
			if d.Name == name {
				if v, ok := last[d.ID]; ok {
					return v, true
				}
			}
		}
		e.mu.RLock()
		defer e.mu.RUnlock()
		if o, ok := e.alarms[name+"|"+a.Chart]; ok {
			return o.Value, true
		}
		return 0, false
	}
}

// Alarms returns a snapshot of all alarm instances (active first, by id).
func (e *Engine) Alarms() []Alarm {
	e.mu.RLock()
	out := make([]Alarm, 0, len(e.alarms))
	now := e.now().Unix()
	allSilent := (e.silenceAll && (e.silenceUntil == 0 || now < e.silenceUntil)) || e.inMaintenanceLocked(e.now())
	for _, a := range e.alarms {
		cp := *a
		if allSilent || e.plans.Matches(a.Chart, a.Name, now) {
			cp.Silenced = true
		} else {
			for _, key := range []string{a.Chart + "." + a.Name, a.Name} {
				if until, ok := e.silenced[key]; ok && (until == 0 || now < until) {
					cp.Silenced = true
					break
				}
			}
		}
		out = append(out, cp)
	}
	e.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Active != out[j].Active {
			return out[i].Active
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Log returns entries with UniqueID > after (all when after == 0), oldest first.
func (e *Engine) Log(after uint64) []LogEntry {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]LogEntry, 0, len(e.entries))
	for _, le := range e.entries {
		if le.UniqueID > after {
			out = append(out, le)
		}
	}
	return out
}

// Summary counts alarms per status.
type Summary struct {
	Normal   int `json:"normal"`
	Warning  int `json:"warning"`
	Critical int `json:"critical"`
	Silent   int `json:"silent"`
}

func (e *Engine) Summary() Summary {
	var s Summary
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, a := range e.alarms {
		if !a.Active {
			continue
		}
		switch a.Status {
		case StatusClear:
			s.Normal++
		case StatusWarning:
			s.Warning++
		case StatusCritical:
			s.Critical++
		default:
			s.Silent++
		}
	}
	return s
}

// IsSilenced reports whether notifications for this alarm are currently suppressed.
func (e *Engine) IsSilenced(chart, name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.suppressionReasonLocked(chart, name) != ""
}

func (e *Engine) suppressionReasonLocked(chart, name string) string {
	now := e.now().Unix()
	if e.plans.Matches(chart, name, now) {
		return "planned_maintenance"
	}
	if e.inMaintenanceLocked(e.now()) {
		return "maintenance"
	}
	if e.silenceAll {
		if e.silenceUntil == 0 || now < e.silenceUntil {
			return "global_silence"
		}
		e.silenceAll, e.silenceUntil = false, 0
	}
	for _, key := range []string{chart + "." + name, name} {
		if until, ok := e.silenced[key]; ok {
			if until == 0 || now < until {
				return "alarm_silence"
			}
			delete(e.silenced, key)
		}
	}
	return ""
}

// ApplySilence updates runtime silence. until<0 is seconds from now; 0 = forever.
// all=true silences every alarm; key silences one name or chart.name; clear removes it.
func (e *Engine) ApplySilence(all *bool, key string, until int64, clear bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if until < 0 {
		until = e.now().Unix() - until
	}
	if all != nil {
		if clear || !*all {
			e.silenceAll, e.silenceUntil = false, 0
		} else {
			e.silenceAll, e.silenceUntil = true, until
		}
	}
	if key != "" {
		if e.silenced == nil {
			e.silenced = map[string]int64{}
		}
		if clear {
			delete(e.silenced, key)
		} else {
			e.silenced[key] = until
		}
	}
}

// SilenceInfo is the GET /api/v1/alarms/silence payload.
func (e *Engine) SilenceInfo() map[string]any {
	e.mu.RLock()
	defer e.mu.RUnlock()
	now := e.now().Unix()
	all := e.silenceAll && (e.silenceUntil == 0 || now < e.silenceUntil)
	alarms := map[string]int64{}
	for k, v := range e.silenced {
		if v == 0 || now < v {
			alarms[k] = v
		}
	}
	return map[string]any{"all": all, "until": e.silenceUntil, "alarms": alarms, "maintenance": e.inMaintenanceLocked(e.now()), "maint_until": e.maintUntil}
}

// ChartVariables returns $names available to alarm expressions for a chart.
func (e *Engine) ChartVariables(chartID, alarmName string) map[string]any {
	out := map[string]any{}
	for k, v := range e.opt.HostVars {
		out[k] = v
	}
	if c, ok := e.reg.Chart(chartID); ok {
		_, last := c.LastValues()
		for k, v := range last {
			out[k] = v
		}
		for _, d := range c.Dims() {
			if v, ok := last[d.ID]; ok {
				out[d.Name] = v
			}
		}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, a := range e.alarms {
		if a.Chart != chartID {
			continue
		}
		if !math.IsNaN(a.Value) {
			out[a.Name] = a.Value
		}
		if alarmName == "" || a.Name == alarmName {
			out["status"] = float64(a.Status)
			if !math.IsNaN(a.Value) {
				out["this"] = a.Value
			}
		}
	}
	return out
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
