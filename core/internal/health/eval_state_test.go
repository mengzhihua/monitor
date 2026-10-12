package health

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestParseEvalState(t *testing.T) {
	cases := map[string]EvalStatePolicy{
		"":          EvalStateUndefined,
		"undefined": EvalStateUndefined,
		"NoData":    EvalStateUndefined,
		"error":     EvalStateUndefined,
		"ok":        EvalStateOK,
		"CLEAR":     EvalStateOK,
		"alerting":  EvalStateAlerting,
		"critical":  EvalStateAlerting,
		"keep_last": EvalStateKeepLast,
		"keep-last": EvalStateKeepLast,
	}
	for raw, want := range cases {
		got, err := parseEvalState("t", "a", "no_data_state", raw)
		if err != nil || got != want {
			t.Fatalf("%q => %q %v, want %q", raw, got, err, want)
		}
	}
	if _, err := parseEvalState("t", "a", "exec_err_state", "maybe"); err == nil || !strings.Contains(err.Error(), "unknown value") {
		t.Fatalf("bad policy error = %v", err)
	}
}

func TestApplyEvalState(t *testing.T) {
	if got := applyEvalState(EvalStateUndefined, StatusWarning); got != StatusUndefined {
		t.Fatalf("undefined = %v", got)
	}
	if got := applyEvalState(EvalStateOK, StatusWarning); got != StatusClear {
		t.Fatalf("ok = %v", got)
	}
	if got := applyEvalState(EvalStateAlerting, StatusClear); got != StatusCritical {
		t.Fatalf("alerting = %v", got)
	}
	if got := applyEvalState(EvalStateKeepLast, StatusWarning); got != StatusWarning {
		t.Fatalf("keep_last = %v", got)
	}
	if got := applyEvalState(EvalStateKeepLast, StatusUninitialized); got != StatusUndefined {
		t.Fatalf("keep_last uninitialized = %v", got)
	}
}

func TestCompileEvalStateFields(t *testing.T) {
	r, err := Compile(RuleSpec{
		Name: "x", On: "system.ram", Lookup: "average -10s of used",
		Warn: "$this > 1", NoDataState: "keep_last", ExecErrState: "alerting",
	}, "t")
	if err != nil {
		t.Fatal(err)
	}
	if r.NoDataState != EvalStateKeepLast || r.ExecErrState != EvalStateAlerting {
		t.Fatalf("compiled = %+v", r)
	}
	if _, err := Compile(RuleSpec{
		Name: "x", On: "system.ram", Calc: "$used", Warn: "$this > 1", NoDataState: "bogus",
	}, "t"); err == nil {
		t.Fatal("expected compile error")
	}
}

func TestNoDataStateAlertingNotifies(t *testing.T) {
	const yaml = `
alarms:
  - name: ram_in_use
    on: system.ram
    lookup: average -10s percentage of used
    units: '%'
    every: 1s
    warn: '$this > 80'
    crit: '$this > 95'
    no_data_state: alerting
    to: sysadmin
`
	n := &memNotifier{}
	e, reg := newTestEngine(t, yaml, n)
	now := time.Unix(1_700_000_000, 0)
	_ = reg.Collect("system.ram", now, map[string]float64{"used": 10, "free": 90})
	now = now.Add(time.Second)
	e.Tick(now)
	if a := e.Alarms()[0]; a.Status != StatusClear {
		t.Fatalf("status = %v, want CLEAR", a.Status)
	}
	now = now.Add(5 * time.Minute) // empty lookup window
	e.Tick(now)
	if a := e.Alarms()[0]; a.Status != StatusCritical {
		t.Fatalf("status = %v, want CRITICAL from no_data_state", a.Status)
	}
	waitDelivered(t, e, 1)
	if n.seen[0].Status != StatusCritical || n.seen[0].OldStatus != StatusClear {
		t.Fatalf("notify = %+v", n.seen[0])
	}
}

func TestNoDataStateKeepLastPreservesWarning(t *testing.T) {
	const yaml = `
alarms:
  - name: ram_in_use
    on: system.ram
    lookup: average -10s percentage of used
    units: '%'
    every: 1s
    warn: '$this > 80'
    crit: '$this > 95'
    no_data_state: keep_last
    to: sysadmin
`
	n := &memNotifier{}
	e, reg := newTestEngine(t, yaml, n)
	now := time.Unix(1_700_000_000, 0)
	feed := func(used, free float64) {
		_ = reg.Collect("system.ram", now, map[string]float64{"used": used, "free": free})
		now = now.Add(time.Second)
		e.Tick(now)
	}
	feed(90, 10) // WARNING
	waitDelivered(t, e, 1)
	now = now.Add(5 * time.Minute)
	e.Tick(now)
	if a := e.Alarms()[0]; a.Status != StatusWarning {
		t.Fatalf("status = %v, want WARNING kept across no-data", a.Status)
	}
	if math.IsNaN(e.Alarms()[0].Value) == false {
		t.Fatalf("value should be NaN during no-data gap, got %v", e.Alarms()[0].Value)
	}
	time.Sleep(20 * time.Millisecond)
	if n.count() != 1 {
		t.Fatalf("keep_last must not re-notify, got %+v", n.seen)
	}
}

func TestNoDataStateOKClearsWithoutNotify(t *testing.T) {
	const yaml = `
alarms:
  - name: ram_in_use
    on: system.ram
    lookup: average -10s percentage of used
    units: '%'
    every: 1s
    warn: '$this > 80'
    no_data_state: ok
    to: sysadmin
`
	n := &memNotifier{}
	e, reg := newTestEngine(t, yaml, n)
	now := time.Unix(1_700_000_000, 0)
	_ = reg.Collect("system.ram", now, map[string]float64{"used": 90, "free": 10})
	now = now.Add(time.Second)
	e.Tick(now)
	waitDelivered(t, e, 1)
	now = now.Add(5 * time.Minute)
	e.Tick(now)
	if a := e.Alarms()[0]; a.Status != StatusClear {
		t.Fatalf("status = %v, want CLEAR from no_data_state=ok", a.Status)
	}
	waitDelivered(t, e, 2)
	if n.seen[1].Status != StatusClear || n.seen[1].OldStatus != StatusWarning {
		t.Fatalf("recovery notify = %+v", n.seen[1])
	}
}

func TestExecErrStateAlerting(t *testing.T) {
	const yaml = `
alarms:
  - name: div_zero
    on: system.ram
    calc: '$used / 0'
    every: 1s
    warn: '$this > 1'
    exec_err_state: alerting
    to: sysadmin
`
	n := &memNotifier{}
	e, reg := newTestEngine(t, yaml, n)
	now := time.Unix(1_700_000_000, 0)
	_ = reg.Collect("system.ram", now, map[string]float64{"used": 10, "free": 90})
	now = now.Add(time.Second)
	e.Tick(now)
	if a := e.Alarms()[0]; a.Status != StatusCritical {
		t.Fatalf("status = %v, want CRITICAL from exec_err_state", a.Status)
	}
	waitDelivered(t, e, 1)
	if n.seen[0].Status != StatusCritical {
		t.Fatalf("notify = %+v", n.seen[0])
	}
}

func TestDefaultNoDataStillUndefined(t *testing.T) {
	n := &memNotifier{}
	e, reg := newTestEngine(t, ramRule, n)
	now := time.Unix(1_700_000_000, 0)
	_ = reg.Collect("system.ram", now, map[string]float64{"used": 10, "free": 90})
	now = now.Add(time.Second)
	e.Tick(now)
	now = now.Add(5 * time.Minute)
	e.Tick(now)
	if a := e.Alarms()[0]; a.Status != StatusUndefined {
		t.Fatalf("default status = %v, want UNDEFINED", a.Status)
	}
	time.Sleep(20 * time.Millisecond)
	if n.count() != 0 {
		t.Fatalf("UNDEFINED must not notify, got %+v", n.seen)
	}
}
