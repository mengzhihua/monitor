package health

import (
	"errors"
	"testing"
	"time"
)

func TestManualClose(t *testing.T) {
	e, reg := newTestEngine(t, ramRule)
	defer e.Close()
	now := time.Unix(1_700_000_000, 0)
	reg.Collect("system.ram", now, map[string]float64{"used": 950, "free": 50})
	e.Tick(now)

	var raised *Alarm
	for _, a := range e.Alarms() {
		cp := a
		if cp.Status >= StatusWarning {
			raised = &cp
		}
	}
	if raised == nil {
		t.Fatal("alarm did not raise")
	}

	if err := e.CloseAlarm(raised.ID+9999, "ops", "nope"); !errors.Is(err, ErrAlarmNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	if err := e.CloseAlarm(raised.ID, "ops", "acknowledged, silencing"); err != nil {
		t.Fatalf("close: %v", err)
	}
	var got *Alarm
	for _, a := range e.Alarms() {
		if a.ID == raised.ID {
			cp := a
			got = &cp
		}
	}
	if got == nil || got.Status != StatusClear {
		t.Fatalf("status after close: %+v", got)
	}
	entries := e.Log(0)
	var last *LogEntry
	for i := range entries {
		if entries[i].AlarmID == raised.ID && entries[i].Manual {
			last = &entries[i]
		}
	}
	if last == nil || last.Status != StatusClear || last.User != "ops" || last.Comment != "acknowledged, silencing" {
		t.Fatalf("manual close entry: %+v", last)
	}
	if err := e.CloseAlarm(raised.ID, "ops", "again"); !errors.Is(err, ErrAlarmNotRaised) {
		t.Fatalf("double close: %v", err)
	}

	// The condition still holds: next evaluation re-raises like Zabbix.
	reg.Collect("system.ram", now.Add(2*time.Second), map[string]float64{"used": 950, "free": 50})
	e.Tick(now.Add(2 * time.Second))
	for _, a := range e.Alarms() {
		if a.ID == raised.ID && a.Status < StatusWarning {
			t.Fatalf("expected re-raise, got %s", a.Status)
		}
	}
}
