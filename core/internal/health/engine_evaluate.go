package health

import (
	"math"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// ruleEvaluation separates expression results from committing an alarm state.
// It carries the no-data/error distinction through the recovery and sustain
// gates without performing log writes or notification delivery.
type ruleEvaluation struct {
	value         float64
	status        Status
	policyApplied bool
	recovered     bool
}

// lookup snapshots the replaceable anomaly provider under the engine lock;
// database reads and reductions belong to the independent metric reader.
func (e *Engine) lookup(c *registry.Chart, l *Lookup, now time.Time) float64 {
	e.mu.RLock()
	src := e.anomaly
	e.mu.RUnlock()
	return (metricLookup{db: e.db, anomaly: src}).value(c, l, now)
}

// evaluate runs one alarm: compute outside mu, commit under mu, then publish.
// Tick holds tickMu across all three stages, as does manual close/rule mutation.
func (e *Engine) evaluate(a *Alarm, now time.Time) {
	result := e.evaluateRule(a, now)
	status := result.status
	e.mu.Lock()
	a.nextRun = now.Add(a.rule.Every)
	old, oldValue := a.Status, a.Value
	a.Value = result.value
	a.LastUpdated = now.Unix()
	if result.policyApplied {
		// keep_last of a raised status must not reset sustain / hold timers
		// during a brief gap; any other policy outcome clears them.
		if !(status == old && (old == StatusWarning || old == StatusCritical)) {
			a.clearSustain()
		}
	} else {
		status = gateRecovery(a, status, result.recovered)
		status = gateRaised(a, status, now)
	}
	if status != old {
		a.prevStatusChange = a.LastStatusChange
		a.Status = status
		a.LastStatusChange = now.Unix()
		a.actionFired = 0
		a.actionInhibitNoted = 0
		entry := e.newEntry(a, old, oldValue, now)
		e.mu.Unlock()
		e.transition(a, entry, now)
		return
	}
	// multiple: the problem expression is still true, so record another event
	// without moving the original raise time.
	if a.rule.Multiple && !result.policyApplied && result.status == old && (old == StatusWarning || old == StatusCritical) {
		entry := e.newEntry(a, old, oldValue, now)
		entry.Multiple = true
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

// evaluateRule captures the alarm status and latest chart sample once. Each
// expression in this evaluation sees the same status, timestamp and values.
func (e *Engine) evaluateRule(a *Alarm, now time.Time) ruleEvaluation {
	r := a.rule
	e.mu.RLock()
	previous := a.Status
	e.mu.RUnlock()
	lookupValue := math.NaN()
	if r.Lookup != nil {
		lookupValue = e.lookup(a.chart, r.Lookup, now)
	}
	value := lookupValue
	// Do not combine a fresh collection timestamp with a cached dimension.
	// Historical lookups retain their explicitly configured window semantics.
	lastT, last := a.chart.LatestValues()
	vars := e.variables(a, previous, value, lastT, last, now)
	if r.Calc != nil {
		value = r.Calc.Eval(vars)
		vars = e.variables(a, previous, value, lastT, last, now)
	}
	return evaluateRuleState(r, previous, lookupValue, value, vars)
}

// evaluateRuleState classifies the result without changing runtime state.
// No-data/error policy outcomes deliberately bypass recovery/sustain gates.
func evaluateRuleState(r *Rule, previous Status, lookupValue, value float64, vars func(string) (float64, bool)) ruleEvaluation {
	result := ruleEvaluation{value: value, recovered: true}
	hasLookup := r.Lookup != nil
	lookupNoData := hasLookup && math.IsNaN(lookupValue)
	lookupInf := hasLookup && math.IsInf(lookupValue, 0)
	if math.IsNaN(value) || math.IsInf(value, 0) {
		// An empty lookup window may cascade into a NaN calc. A failing calc
		// with samples, a calc-only failure, or an infinite lookup is an error.
		policy := r.NoDataState
		if lookupInf || (r.Calc != nil && !lookupNoData) {
			policy = r.ExecErrState
		}
		result.policyApplied = true
		result.status = applyEvalState(policy, previous)
		return result
	}
	switch {
	case r.Crit != nil && truthy(r.Crit.Eval(vars)):
		result.status = StatusCritical
	case r.Warn != nil && truthy(r.Warn.Eval(vars)):
		result.status = StatusWarning
	default:
		result.status = StatusClear
	}
	// Recovery only gates a clear result when the alarm is already raised.
	if r.Recovery != nil && result.status == StatusClear && (previous == StatusWarning || previous == StatusCritical) {
		result.recovered = truthy(r.Recovery.Eval(vars))
	}
	return result
}

// variables resolves $names for expressions: alarm constants, $this,
// $status, the chart's latest dimension values, and other alarms of the chart.
func (e *Engine) variables(a *Alarm, status Status, this float64, lastT int64, last map[string]float64, now time.Time) func(string) (float64, bool) {
	return func(name string) (float64, bool) {
		switch name {
		case "this":
			return this, true
		case "status":
			return float64(status), true
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
