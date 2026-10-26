package health

// IsSilenced reports whether notifications for this alarm are currently suppressed.
func (e *Engine) IsSilenced(chart, name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.suppressionReasonLocked(chart, name) != ""
}

func (e *Engine) suppressionReasonLocked(chart, name string) string {
	now := e.now().Unix()
	alarm := e.alarms[name+"|"+chart]
	var labels map[string]string
	if alarm != nil {
		labels = alarm.Labels
	}
	if e.plans.MatchesLabels(chart, name, labels, now) {
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
