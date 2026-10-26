package health

import (
	"context"
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
	// Actions are multi-step escalation policies. RunCommand executes a
	// whitelisted command; it must not call back into the engine.
	Actions    []Action
	RunCommand func(ActionCommand)
	// OnActionInhibited records a command step that same-chart (or similar)
	// inhibit skipped before applyActionLocked. It must not call back into
	// the engine.
	OnActionInhibited func(ActionCommand, string)
	Correlation       []CorrelationRule
}

// ActionCommand is one auto-remediation invocation fired by an escalation step.
type ActionCommand struct {
	Name          string // whitelisted command name
	AlarmID       uint64
	AlarmName     string
	Chart         string
	Hostname      string
	EpisodeStatus Status // handling-episode severity (raised status, even for recovery)
	EpisodeSince  int64  // LastStatusChange of that raised episode
	Recovery      bool
}

// AnomalySource is implemented by the ML collector (duck-typed; no import cycle).
type AnomalySource interface {
	Rate(chart, dim string) (float64, bool)
	RatesBetween(chart, dim string, after, before int64) []float64
}

// SetActive reports whether this process should deliver notifications.
// A nil function, or a nil engine, keeps the current behavior. The function
// runs while the engine lock is held and must not call back into the engine.
func (e *Engine) SetActive(active func() bool) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.active = active
	e.mu.Unlock()
}

// Engine evaluates rules and tracks alarms for one host.
//
// tickMu serializes a complete evaluation cycle, rule mutation and manual
// close. mu protects mutable alarm/notification state and reader snapshots;
// callers taking both locks must acquire tickMu first. Database lookups and
// external notifications execute without mu held.
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
	active      func() bool          // guarded by mu; nil means this process may notify
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
	actions      []compiledAction
	correlation  []compiledCorrelation
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
	actions, err := compileActions(opt.Actions)
	if err != nil {
		return nil, err
	}
	correlation, err := compileCorrelation(opt.Correlation)
	if err != nil {
		return nil, err
	}
	e := &Engine{opt: opt, reg: reg, db: db, log: opt.Logger, now: opt.Now, rules: opt.Rules,
		alarms: map[string]*Alarm{}, nextID: 1, nextLog: 1, notifyCh: make(chan LogEntry, 256),
		silenceAll: opt.SilenceAll, silenced: map[string]int64{}, enabled: true, windows: opt.Windows,
		anomaly: opt.Anomaly, inhibit: inhibit, actions: actions, correlation: correlation}
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
