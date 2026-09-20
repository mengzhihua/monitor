package health

import (
	"bufio"
	"context"
	"encoding/json"
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

	// notification pacing
	DelayUpTo    int64 `json:"delay_up_to_timestamp,omitempty"`
	LastNotified int64 `json:"last_notified,omitempty"`

	rule        *Rule
	chart       *registry.Chart
	nextRun     time.Time
	pending     *LogEntry // transition waiting for its delay to expire
	notifiedSt  Status    // status last reported to notifiers (or silently settled)
	delayMult   float64
	lastDelayAt time.Time
}

// LogEntry records a status transition (or a repeat notification).
type LogEntry struct {
	UniqueID   uint64  `json:"unique_id"`
	AlarmID    uint64  `json:"alarm_id"`
	When       int64   `json:"when"`
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
}

// Notifier delivers alarm transitions somewhere (Slack, email, webhook...).
type Notifier interface {
	Name() string
	Notify(ctx context.Context, e LogEntry) error
}

// Options configures the engine.
type Options struct {
	Rules      []*Rule
	Hostname   string
	LogDir     string // alarm-log.jsonl lives here; empty = memory only
	LogKeep    int    // entries kept in memory (default 1000)
	Notifiers  []Notifier
	HostVars   map[string]float64  // e.g. cpus, ram_total (MiB); referenced as $cpus
	Roles      map[string][]string // recipient role -> notifier names; "" role = all
	Logger     *slog.Logger
	OnEvent    func(e LogEntry) // called on every transition (for the live WS)
	Now        func() time.Time
	SilenceAll bool
}

// Engine evaluates rules and tracks alarms for one host.
type Engine struct {
	opt   Options
	reg   *registry.Registry
	db    *tsdb.Store
	log   *slog.Logger
	now   func() time.Time
	rules []*Rule

	mu      sync.RWMutex
	alarms  map[string]*Alarm // rule name|chart id
	entries []LogEntry
	nextID  uint64
	nextLog uint64
	logFile *os.File

	notifyCh   chan LogEntry
	notified   atomic.Int64
	dispatchWG sync.WaitGroup
	startOnce  sync.Once
	closeOnce  sync.Once
	closed     bool // notifyCh closed; guarded by mu
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
	e := &Engine{opt: opt, reg: reg, db: db, log: opt.Logger, now: opt.Now, rules: opt.Rules,
		alarms: map[string]*Alarm{}, nextID: 1, nextLog: 1, notifyCh: make(chan LogEntry, 256)}
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

// Rules returns the compiled rule set.
func (e *Engine) Rules() []*Rule { return e.rules }

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
		e.mu.Lock()
		e.closed = true
		close(e.notifyCh)
		e.mu.Unlock()
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
			if c.ID != r.Spec.On && c.Context != r.Spec.On {
				continue
			}
			if !labelsMatch(r.Spec.Labels, c.Labels) {
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
				Units: r.Spec.Units, Info: r.Spec.Info, Lookup: r.Spec.Lookup, Calc: r.Spec.Calc,
				Warn: r.Spec.Warn, Crit: r.Spec.Crit, Every: int64(r.Every / time.Second),
				Recipient: r.Spec.To, Source: r.Source, Labels: c.Labels,
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

	e.mu.Lock()
	a.nextRun = now.Add(r.Every)
	old, oldValue := a.Status, a.Value
	a.Value = value
	a.LastUpdated = now.Unix()
	if status != old {
		a.Status = status
		a.LastStatusChange = now.Unix()
		entry := e.newEntry(a, old, oldValue, now)
		e.mu.Unlock()
		e.transition(a, entry, now)
		return
	}
	// repeat notifications while raised
	if rep := e.repeatAfter(a); rep > 0 && a.LastNotified > 0 && now.Unix()-a.LastNotified >= int64(rep/time.Second) {
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
	// but never notified; a rule that starts out raised is.
	e.mu.Lock()
	silent := entry.Status <= StatusUndefined ||
		(entry.Status == StatusClear && a.notifiedSt <= StatusClear) ||
		entry.Status == a.notifiedSt
	if silent {
		a.pending = nil
		a.DelayUpTo = 0
		a.notifiedSt = entry.Status
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
		e.notify(saved)
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
		e.notify(p)
	}
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
		e.notify(entry)
	}
	return entry
}

// notify queues an entry for delivery. Delivery state (Notified, NotifiedAt,
// Alarm.LastNotified) is only recorded once a notifier actually succeeds.
func (e *Engine) notify(entry LogEntry) {
	if e.opt.SilenceAll || len(e.notifiersFor(entry.Recipient)) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	select {
	case e.notifyCh <- entry:
	default:
		e.log.Warn("notification queue full, dropping", "alarm", entry.Name)
	}
}

func (e *Engine) dispatch() {
	for entry := range e.notifyCh {
		delivered := false
		for _, n := range e.notifiersFor(entry.Recipient) {
			cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := n.Notify(cctx, entry)
			cancel()
			if err != nil {
				e.log.Error("notify failed", "via", n.Name(), "alarm", entry.Name, "err", err)
				continue
			}
			delivered = true
			e.notified.Add(1)
		}
		if delivered {
			e.markNotified(entry)
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
	var sum, total float64
	var matched bool
	var minSum, maxSum float64
	for _, d := range dims {
		pts, err := e.db.Query(registry.SeriesID(c.ID, d.ID), after, before)
		if err != nil || len(pts) == 0 {
			continue
		}
		vals := make([]float64, 0, len(pts))
		for _, p := range pts {
			if !math.IsNaN(p.Value) {
				vals = append(vals, p.Value)
			}
		}
		if len(vals) == 0 {
			continue
		}
		var v float64
		if l.Method == tsdb.GroupSum {
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
	for _, a := range e.alarms {
		out = append(out, *a)
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
