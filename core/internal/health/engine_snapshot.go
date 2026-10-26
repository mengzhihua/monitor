package health

import (
	"math"
	"sort"
)

// Alarms returns a snapshot of all alarm instances (active first, by id).
func (e *Engine) Alarms() []Alarm {
	e.mu.RLock()
	out := make([]Alarm, 0, len(e.alarms))
	now := e.now().Unix()
	allSilent := (e.silenceAll && (e.silenceUntil == 0 || now < e.silenceUntil)) || e.inMaintenanceLocked(e.now())
	for _, a := range e.alarms {
		cp := *a
		if allSilent || e.plans.MatchesLabels(a.Chart, a.Name, a.Labels, now) {
			cp.Silenced = true
		} else {
			for _, key := range []string{a.Chart + "." + a.Name, a.Name} {
				if until, ok := e.silenced[key]; ok && (until == 0 || now < until) {
					cp.Silenced = true
					break
				}
			}
		}
		cp.Cause = e.correlatedCauseLocked(a)
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

// ChartVariables returns $names available to alarm expressions for a chart.
func (e *Engine) ChartVariables(chartID, alarmName string) map[string]any {
	out := map[string]any{}
	for k, v := range e.opt.HostVars {
		out[k] = v
	}
	if c, ok := e.reg.Chart(chartID); ok {
		_, last := c.LatestValues()
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
