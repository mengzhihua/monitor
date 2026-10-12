package health

import (
	"fmt"
	"strings"
)

// EvalStatePolicy maps a no-data or evaluation-error outcome to an alarm
// status. Values follow Grafana's noDataState / execErrState vocabulary,
// adapted to Monitor's Netdata-style statuses.
type EvalStatePolicy string

const (
	// EvalStateUndefined keeps the historical behavior: status becomes
	// UNDEFINED and notifications are not sent for that transition.
	EvalStateUndefined EvalStatePolicy = "undefined"
	// EvalStateOK treats the outcome as CLEAR (Grafana "OK").
	EvalStateOK EvalStatePolicy = "ok"
	// EvalStateAlerting treats the outcome as CRITICAL (Grafana "Alerting").
	EvalStateAlerting EvalStatePolicy = "alerting"
	// EvalStateKeepLast retains the previous committed status. UNINITIALIZED
	// and REMOVED fall back to UNDEFINED so a brand-new alarm does not stay
	// stuck before the first successful evaluation.
	EvalStateKeepLast EvalStatePolicy = "keep_last"
)

func parseEvalState(source, name, field, raw string) (EvalStatePolicy, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "", "undefined", "nodata", "no_data", "error":
		// empty / undefined: Netdata default. "nodata"/"error" are accepted as
		// aliases of undefined for readers coming from Grafana's NoData/Error.
		return EvalStateUndefined, nil
	case "ok", "clear", "normal":
		return EvalStateOK, nil
	case "alerting", "alert", "critical":
		return EvalStateAlerting, nil
	case "keep_last", "keeplast", "keep-last":
		return EvalStateKeepLast, nil
	default:
		return "", fmt.Errorf("%s: alarm %q %s: unknown value %q (want undefined|ok|alerting|keep_last)", source, name, field, raw)
	}
}

func applyEvalState(policy EvalStatePolicy, last Status) Status {
	switch policy {
	case EvalStateOK:
		return StatusClear
	case EvalStateAlerting:
		return StatusCritical
	case EvalStateKeepLast:
		if last == StatusUninitialized || last == StatusRemoved {
			return StatusUndefined
		}
		return last
	default:
		return StatusUndefined
	}
}
