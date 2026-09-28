package health

import "time"

// gateRaised applies Prometheus-style for and keep_firing_for. for holds a
// rise at the previous status until the new warning or critical state has
// been continuous. keep_firing_for holds a raised status after the condition
// returns to clear. Notification delay is unchanged and starts only after
// the status itself commits. No-data stays immediate.
func gateRaised(a *Alarm, want Status, now time.Time) Status {
	if want <= StatusUndefined || a == nil || a.rule == nil {
		if a != nil {
			a.clearSustain()
		}
		return want
	}
	r := a.rule
	if (want == StatusWarning || want == StatusCritical) && want > a.Status && r.For > 0 {
		if a.sustainStatus != want {
			a.sustainStatus = want
			a.sustainSince = now
			a.SustainStatus = want
			a.SustainSince = now.Unix()
		}
		a.PendingUntil = a.sustainSince.Add(r.For).Unix()
		a.holdUntil = time.Time{}
		a.HoldUntil = 0
		if now.Sub(a.sustainSince) < r.For {
			if a.Status <= StatusUndefined {
				return StatusClear
			}
			return a.Status
		}
		a.clearSustain()
		return want
	}
	if want == a.Status || (a.Status <= StatusUndefined && want == StatusClear) {
		a.clearSustain()
		return want
	}
	if want == StatusClear && (a.Status == StatusWarning || a.Status == StatusCritical) && r.KeepFiring > 0 {
		a.clearPendingRaise()
		if a.holdUntil.IsZero() {
			a.holdUntil = now.Add(r.KeepFiring)
			a.HoldUntil = a.holdUntil.Unix()
		}
		if now.Before(a.holdUntil) {
			return a.Status
		}
		a.holdUntil = time.Time{}
		a.HoldUntil = 0
		return StatusClear
	}
	a.clearSustain()
	return want
}

func (a *Alarm) clearPendingRaise() {
	a.sustainStatus = 0
	a.sustainSince = time.Time{}
	a.SustainStatus = 0
	a.SustainSince = 0
	a.PendingUntil = 0
}

func (a *Alarm) clearSustain() {
	a.clearPendingRaise()
	a.holdUntil = time.Time{}
	a.HoldUntil = 0
}
