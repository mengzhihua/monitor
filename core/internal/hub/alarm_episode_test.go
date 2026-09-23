package hub

import (
	"testing"

	"github.com/mengzhihua/monitor/core/internal/health"
)

func TestAlarmReminderKeepsEpisodeStart(t *testing.T) {
	n := &Node{alarms: map[string]health.LogEntry{}}
	e := health.LogEntry{AlarmID: 1, UniqueID: 1, Chart: "system.cpu", Name: "cpu", Status: health.StatusWarning, When: 100}
	n.recordAlarm(e)
	e.UniqueID = 2
	e.When = 200
	e.Repeat = true
	n.recordAlarm(e)
	a := n.Alarms()[0]
	if a.When != 100 || a.Updated != 200 {
		t.Fatalf("reminder changed episode: %+v", a)
	}
	if log := n.AlarmLog(0); len(log) != 2 || log[1].When != 200 || !log[1].Repeat {
		t.Fatalf("reminder log lost: %+v", log)
	}
	e.UniqueID = 3
	e.When = 300
	e.Repeat = false
	e.Status = health.StatusCritical
	n.recordAlarm(e)
	if got := n.Alarms()[0]; got.When != 300 {
		t.Fatal("escalation did not create new episode")
	}
}
