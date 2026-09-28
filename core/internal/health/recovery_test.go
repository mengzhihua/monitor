package health

import (
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

const recoveryRule = `
alarms:
  - name: ram_hot
    on: system.ram
    calc: $used
    every: 1s
    warn: '$this > 80'
    crit: '$this > 90'
    recovery: '$this < 70'
    to: sysadmin
`

func TestRecoveryExpressionHoldsUntilClear(t *testing.T) {
	e, reg := newTestEngine(t, recoveryRule, &memNotifier{})
	now := time.Unix(1_700_000_200, 0)
	set := func(at time.Time, used float64) {
		t.Helper()
		if err := reg.Collect("system.ram", at, map[string]float64{"used": used, "free": 100 - used}); err != nil {
			t.Fatal(err)
		}
		e.Tick(at)
	}
	set(now, 85)
	waitDelivered(t, e, 1)
	a := e.Alarms()[0]
	if a.Status != StatusWarning || a.RecoveryHold {
		t.Fatalf("raise = %+v", a.Status)
	}
	set(now.Add(time.Second), 75)
	a = e.Alarms()[0]
	if a.Status != StatusWarning || !a.RecoveryHold || e.Notified() != 1 {
		t.Fatalf("hold warning = status %v hold %v notified %d", a.Status, a.RecoveryHold, e.Notified())
	}
	set(now.Add(2*time.Second), 95)
	if e.Alarms()[0].Status != StatusCritical || e.Alarms()[0].RecoveryHold {
		t.Fatalf("crit = %+v hold %v", e.Alarms()[0].Status, e.Alarms()[0].RecoveryHold)
	}
	set(now.Add(3*time.Second), 75)
	a = e.Alarms()[0]
	if a.Status != StatusCritical || !a.RecoveryHold {
		t.Fatalf("hold critical = %v hold %v", a.Status, a.RecoveryHold)
	}
	set(now.Add(4*time.Second), 85)
	if e.Alarms()[0].Status != StatusWarning || e.Alarms()[0].RecoveryHold {
		t.Fatalf("warn band = %v hold %v", e.Alarms()[0].Status, e.Alarms()[0].RecoveryHold)
	}
	set(now.Add(5*time.Second), 65)
	if e.Alarms()[0].Status != StatusClear || e.Alarms()[0].RecoveryHold {
		t.Fatalf("clear = %v hold %v", e.Alarms()[0].Status, e.Alarms()[0].RecoveryHold)
	}
	waitDelivered(t, e, 4)
}

func TestRecoveryDoesNotHoldMissingData(t *testing.T) {
	rule := `
alarms:
  - name: ram_hot
    on: system.ram
    calc: $used / $flag
    every: 1s
    warn: '$this > 80'
    recovery: '$this < 70'
    to: sysadmin
`
	e, reg := newTestEngine(t, rule, &memNotifier{})
	chart, ok := reg.Chart("system.ram")
	if !ok {
		t.Fatal("missing chart")
	}
	chart.AddDimension(&registry.Dimension{ID: "flag"})
	now := time.Unix(1_700_000_400, 0)
	set := func(at time.Time, used, flag float64) {
		t.Helper()
		if err := reg.Collect("system.ram", at, map[string]float64{"used": used, "free": 1, "flag": flag}); err != nil {
			t.Fatal(err)
		}
		e.Tick(at)
	}
	set(now, 85, 1)
	if e.Alarms()[0].Status != StatusWarning {
		t.Fatalf("raise = %v", e.Alarms()[0].Status)
	}
	set(now.Add(time.Second), 85, 0)
	if e.Alarms()[0].Status != StatusUndefined || e.Alarms()[0].RecoveryHold {
		t.Fatalf("nodata = %v hold %v", e.Alarms()[0].Status, e.Alarms()[0].RecoveryHold)
	}
}

func TestRecoveryCannotClearWhileProblemExpressionMatches(t *testing.T) {
	rule := `
alarms:
  - name: ram_hot
    on: system.ram
    calc: $used
    every: 1s
    warn: '$this > 80'
    recovery: '$this < 100'
    to: sysadmin
`
	e, reg := newTestEngine(t, rule, &memNotifier{})
	now := time.Unix(1_700_000_500, 0)
	if err := reg.Collect("system.ram", now, map[string]float64{"used": 85, "free": 15}); err != nil {
		t.Fatal(err)
	}
	e.Tick(now)
	if e.Alarms()[0].Status != StatusWarning || e.Alarms()[0].RecoveryHold {
		t.Fatalf("still warning = %v hold %v", e.Alarms()[0].Status, e.Alarms()[0].RecoveryHold)
	}
	if err := reg.Collect("system.ram", now.Add(time.Second), map[string]float64{"used": 75, "free": 25}); err != nil {
		t.Fatal(err)
	}
	e.Tick(now.Add(time.Second))
	if e.Alarms()[0].Status != StatusClear || e.Alarms()[0].RecoveryHold {
		t.Fatalf("cleared = %v hold %v", e.Alarms()[0].Status, e.Alarms()[0].RecoveryHold)
	}
}

func TestRecoveryThenKeepFiring(t *testing.T) {
	rule := `
alarms:
  - name: ram_hot
    on: system.ram
    calc: $used
    every: 1s
    warn: '$this > 80'
    recovery: '$this < 70'
    keep_firing_for: 10s
    to: sysadmin
`
	e, reg := newTestEngine(t, rule, &memNotifier{})
	now := time.Unix(1_700_000_300, 0)
	set := func(at time.Time, used float64) {
		t.Helper()
		if err := reg.Collect("system.ram", at, map[string]float64{"used": used, "free": 100 - used}); err != nil {
			t.Fatal(err)
		}
		e.Tick(at)
	}
	set(now, 85)
	set(now.Add(time.Second), 75)
	held := e.Alarms()[0]
	if held.Status != StatusWarning || !held.RecoveryHold || held.HoldUntil != 0 {
		t.Fatalf("before recovery = status %v hold %v until %d", held.Status, held.RecoveryHold, held.HoldUntil)
	}
	cleared := now.Add(2 * time.Second)
	set(cleared, 65)
	a := e.Alarms()[0]
	if a.Status != StatusWarning || a.RecoveryHold || a.HoldUntil != cleared.Add(10*time.Second).Unix() {
		t.Fatalf("keep firing = status %v recovery %v until %d", a.Status, a.RecoveryHold, a.HoldUntil)
	}
	set(cleared.Add(10*time.Second), 65)
	if e.Alarms()[0].Status != StatusClear || e.Alarms()[0].HoldUntil != 0 {
		t.Fatalf("released = %+v", e.Alarms()[0].Status)
	}
}

func TestRecoveryExpressionRejected(t *testing.T) {
	body := "alarms:\n  - name: x\n    on: system.ram\n    calc: $used\n    warn: '$this > 1'\n    recovery: '>>>'\n"
	if _, err := ParseRules([]byte(body), "test"); err == nil {
		t.Fatal("accepted invalid recovery")
	}
}
