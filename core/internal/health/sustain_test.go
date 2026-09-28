package health

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

const sustainRule = `
alarms:
  - name: ram_hot
    on: system.ram
    calc: $used
    every: 1s
    warn: '$this > 80'
    crit: '$this > 90'
    for: 30s
    keep_firing_for: 20s
    to: sysadmin
`

func TestForAndKeepFiringHoldStatus(t *testing.T) {
	n := &memNotifier{}
	e, reg := newTestEngine(t, sustainRule, n)
	now := time.Unix(1_700_000_000, 0)
	set := func(at time.Time, used float64) {
		t.Helper()
		if err := reg.Collect("system.ram", at, map[string]float64{"used": used, "free": 100 - used}); err != nil {
			t.Fatal(err)
		}
		e.Tick(at)
	}
	set(now, 85)
	a := e.Alarms()[0]
	if a.Status != StatusClear || a.SustainStatus != StatusWarning || a.SustainSince != now.Unix() || a.PendingUntil != now.Add(30*time.Second).Unix() {
		t.Fatalf("pending = status %v pending %v since %d until %d", a.Status, a.SustainStatus, a.SustainSince, a.PendingUntil)
	}
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"pending_status":"WARNING"`)) || !bytes.Contains(raw, []byte(fmt.Sprintf(`"pending_until":%d`, a.PendingUntil))) {
		t.Fatalf("pending json %s", raw)
	}
	set(now.Add(10*time.Second), 10)
	if e.Alarms()[0].Status != StatusClear || e.Alarms()[0].SustainStatus != 0 || e.Alarms()[0].PendingUntil != 0 || e.Notified() != 0 {
		t.Fatalf("blip committed: status %v pending %v notified %d", e.Alarms()[0].Status, e.Alarms()[0].SustainStatus, e.Notified())
	}

	start := now.Add(11 * time.Second)
	set(start, 85)
	set(start.Add(29*time.Second), 85)
	if e.Alarms()[0].Status != StatusClear || e.Alarms()[0].PendingUntil != start.Add(30*time.Second).Unix() || e.Notified() != 0 {
		t.Fatalf("raised early: %v until %d notified %d", e.Alarms()[0].Status, e.Alarms()[0].PendingUntil, e.Notified())
	}
	set(start.Add(30*time.Second), 85)
	if e.Alarms()[0].Status != StatusWarning || e.Alarms()[0].SustainStatus != 0 || e.Alarms()[0].PendingUntil != 0 {
		t.Fatalf("for did not commit: %+v", e.Alarms()[0])
	}
	waitDelivered(t, e, 1)

	set(start.Add(31*time.Second), 95)
	if e.Alarms()[0].Status != StatusWarning || e.Alarms()[0].SustainStatus != StatusCritical || e.Alarms()[0].PendingUntil != start.Add(61*time.Second).Unix() {
		t.Fatalf("critical pending = %+v", e.Alarms()[0])
	}
	set(start.Add(61*time.Second), 95)
	if e.Alarms()[0].Status != StatusCritical {
		t.Fatalf("critical = %v", e.Alarms()[0].Status)
	}
	waitDelivered(t, e, 2)

	cleared := start.Add(62 * time.Second)
	set(cleared, 10)
	held := e.Alarms()[0]
	if held.Status != StatusCritical || held.HoldUntil != cleared.Add(20*time.Second).Unix() || held.PendingUntil != 0 {
		t.Fatalf("hold = status %v until %d pending %d", held.Status, held.HoldUntil, held.PendingUntil)
	}
	set(cleared.Add(19*time.Second), 10)
	if e.Alarms()[0].Status != StatusCritical || e.Notified() != 2 {
		t.Fatalf("cleared early: %v notified %d", e.Alarms()[0].Status, e.Notified())
	}
	set(cleared.Add(20*time.Second), 10)
	if e.Alarms()[0].Status != StatusClear || e.Alarms()[0].HoldUntil != 0 {
		t.Fatalf("release = %+v", e.Alarms()[0])
	}
	waitDelivered(t, e, 3)
}

func TestForLeavesNotificationDelayIntact(t *testing.T) {
	rule := `
alarms:
  - name: ram_hot
    on: system.ram
    calc: $used
    every: 1s
    warn: '$this > 80'
    for: 10s
    delay: up 1m max 1m
    to: sysadmin
`
	e, reg := newTestEngine(t, rule, &memNotifier{})
	now := time.Unix(1_700_000_100, 0)
	if err := reg.Collect("system.ram", now, map[string]float64{"used": 85, "free": 15}); err != nil {
		t.Fatal(err)
	}
	e.Tick(now)
	e.Tick(now.Add(10 * time.Second))
	a := e.Alarms()[0]
	if a.Status != StatusWarning || a.DelayUpTo != now.Add(10*time.Second+time.Minute).Unix() || e.Notified() != 0 {
		t.Fatalf("status %v delay %d notified %d", a.Status, a.DelayUpTo, e.Notified())
	}
}

func TestSustainDurationRejected(t *testing.T) {
	for _, raw := range []string{"for: 30h", "keep_firing_for: nope", "for: -1s"} {
		body := "alarms:\n  - name: x\n    on: system.ram\n    calc: $used\n    warn: '$this > 1'\n    " + raw + "\n"
		if _, err := ParseRules([]byte(body), "test"); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
