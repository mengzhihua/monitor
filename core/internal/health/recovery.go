package health

// gateRecovery applies a Zabbix recovery expression. While warn and crit are
// both false, a raised alarm stays at its committed severity until recovery
// is true. A true warn or crit expression still selects that severity.
// Missing data is decided by the caller and is not held here.
func gateRecovery(a *Alarm, want Status, recovered bool) Status {
	if a == nil {
		return want
	}
	hold := a.rule != nil && a.rule.Recovery != nil &&
		want == StatusClear &&
		(a.Status == StatusWarning || a.Status == StatusCritical) &&
		!recovered
	a.RecoveryHold = hold
	if hold {
		return a.Status
	}
	return want
}
