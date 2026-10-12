package health

import (
	"testing"
	"time"
)

func TestActionEscalatesAndRunsCommandOnce(t *testing.T) {
	cmds := make(chan ActionCommand, 4)
	e := diagnosticsEngine(t, Options{
		Notifiers: []Notifier{diagnosticNotifier{"slack", func() error { return nil }}},
		Roles:     map[string][]string{"sysadmin": {"slack"}, "pager": {"slack"}},
		Actions: []Action{{
			Name: "page", Severities: []string{"CRITICAL"}, Alarms: []string{"disk_*"},
			Steps: []ActionStep{
				{Role: "sysadmin", Command: "note"},
				{Delay: "5m", Role: "pager", Command: "page"},
			},
			Recovery: &ActionStep{Role: "sysadmin", Command: "clear"},
		}},
		RunCommand: func(c ActionCommand) { cmds <- c },
	})
	e.alarms["disk_full|disk.space"] = &Alarm{ID: 7, Name: "disk_full", Chart: "disk.space", Status: StatusCritical, LastStatusChange: 100}
	e.notifyAt(LogEntry{Name: "disk_full", Chart: "disk.space", Status: StatusCritical, When: 100, Recipient: "sysadmin"}, 100)
	got := <-e.notifyCh
	if got.Recipient != "sysadmin" {
		t.Fatalf("first %s", got.Recipient)
	}
	if cmd := <-cmds; cmd.Name != "note" || cmd.AlarmID != 7 || cmd.EpisodeStatus != StatusCritical || cmd.EpisodeSince != 100 {
		t.Fatalf("cmd %+v", cmd)
	}
	e.notifyAt(LogEntry{Name: "disk_full", Chart: "disk.space", Status: StatusCritical, When: 100, Recipient: "sysadmin"}, 100+int64(6*time.Minute/time.Second))
	got = <-e.notifyCh
	if got.Recipient != "pager" {
		t.Fatalf("escalated %s", got.Recipient)
	}
	if cmd := <-cmds; cmd.Name != "page" {
		t.Fatalf("cmd %s", cmd.Name)
	}
	alarm := e.alarms["disk_full|disk.space"]
	alarm.prevStatusChange = 100
	alarm.Status = StatusClear
	alarm.LastStatusChange = 500
	e.notifyAt(LogEntry{Name: "disk_full", Chart: "disk.space", Status: StatusClear, OldStatus: StatusCritical, When: 500}, 500)
	got = <-e.notifyCh
	if got.Recipient != "sysadmin" {
		t.Fatalf("recovery %s", got.Recipient)
	}
	if cmd := <-cmds; cmd.Name != "clear" || !cmd.Recovery || cmd.EpisodeStatus != StatusCritical || cmd.EpisodeSince != 100 {
		t.Fatalf("recovery cmd %+v", cmd)
	}
}

func TestActionChartFilterSkipsOtherCharts(t *testing.T) {
	e := diagnosticsEngine(t, Options{
		Notifiers: []Notifier{diagnosticNotifier{"slack", func() error { return nil }}},
		Actions: []Action{{
			Name: "disk-only", Charts: []string{"disk.*"},
			Steps: []ActionStep{{Role: "pager"}},
		}},
		Roles: map[string][]string{"pager": {"slack"}},
	})
	e.alarms["hot|system.cpu"] = &Alarm{Name: "hot", Chart: "system.cpu", Status: StatusCritical, LastStatusChange: 1}
	e.notifyAt(LogEntry{Name: "hot", Chart: "system.cpu", Status: StatusCritical, When: 1, Recipient: "sysadmin"}, 1)
	if got := <-e.notifyCh; got.Recipient != "sysadmin" {
		t.Fatalf("filtered %s", got.Recipient)
	}
}

func TestCorrelationSuppressesSymptom(t *testing.T) {
	e := diagnosticsEngine(t, Options{
		Notifiers: []Notifier{diagnosticNotifier{"slack", func() error { return nil }}},
		Correlation: []CorrelationRule{{
			Cause: "host_down", Symptoms: []string{"disk_full"},
		}},
	})
	e.alarms["host_down|system.up"] = &Alarm{Name: "host_down", Chart: "system.up", Status: StatusCritical}
	e.alarms["disk_full|disk.space"] = &Alarm{Name: "disk_full", Chart: "disk.space", Status: StatusWarning}
	e.notifyAt(LogEntry{Name: "disk_full", Chart: "disk.space", Status: StatusWarning, When: 5}, 5)
	d := e.NotificationDiagnostics()
	if d.Suppressed != 1 || d.Recent[0].Reason != "correlation" {
		t.Fatalf("%+v", d)
	}
	if cause := e.Alarms(); causeOf(cause, "disk_full") != "host_down" {
		t.Fatalf("%+v", cause)
	}
}

func causeOf(alarms []Alarm, name string) string {
	for _, a := range alarms {
		if a.Name == name {
			return a.Cause
		}
	}
	return ""
}
