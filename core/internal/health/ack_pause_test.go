package health

import (
	"testing"
	"time"
)

func TestAcknowledgePausesRepeatUntilStatusChanges(t *testing.T) {
	const rules = `
alarms:
  - name: ram_in_use
    on: system.ram
    calc: '$used'
    every: 1s
    warn: '$this > 80'
    repeat: warning 1s critical 1s
`
	e, reg := newTestEngine(t, rules)
	now := time.Unix(1_700_000_000, 0)
	_ = reg.Collect("system.ram", now, map[string]float64{"used": 90, "free": 10})
	e.Tick(now.Add(time.Second))
	if err := e.SetAcknowledged("system.ram", "ram_in_use", true, "ops", "looking"); err != nil {
		t.Fatal(err)
	}
	e.Tick(now.Add(5 * time.Second))
	if repeats(e) != 0 {
		t.Fatalf("acknowledged problem repeated: %+v", e.Log(0))
	}
	var acked bool
	for _, alarm := range e.Alarms() {
		if alarm.Name == "ram_in_use" && alarm.Acknowledged && alarm.AckUser == "ops" && alarm.AckNote == "looking" {
			acked = true
		}
	}
	if !acked {
		t.Fatal("alarm was not acknowledged")
	}
	if err := e.SetAcknowledged("system.ram", "ram_in_use", false, "ops", ""); err != nil {
		t.Fatal(err)
	}
	e.Tick(now.Add(10 * time.Second))
	if repeats(e) != 1 {
		t.Fatalf("repeat after unacknowledge = %d", repeats(e))
	}
	_ = reg.Collect("system.ram", now.Add(11*time.Second), map[string]float64{"used": 10, "free": 90})
	e.Tick(now.Add(12 * time.Second))
	for _, alarm := range e.Alarms() {
		if alarm.Name == "ram_in_use" && (alarm.Status != StatusClear || alarm.Acknowledged) {
			t.Fatalf("recovery did not clear acknowledgement: %+v", alarm)
		}
	}
	if err := e.SetAcknowledged("missing", "ram_in_use", true, "ops", ""); err != ErrAlarmNotFound {
		t.Fatalf("missing alarm: %v", err)
	}
}

func repeats(e *Engine) int {
	n := 0
	for _, entry := range e.Log(0) {
		if entry.Repeat {
			n++
		}
	}
	return n
}
