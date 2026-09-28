package health

import (
	"testing"
)

func TestInhibitDependencySuppressesTargetNotification(t *testing.T) {
	e := diagnosticsEngine(t, Options{
		Inhibit:   []InhibitRule{{Source: "host_down", Targets: []string{"http_down", "disk_full"}}},
		Notifiers: []Notifier{diagnosticNotifier{"slack", func() error { return nil }}},
	})
	e.alarms["host_down|system.up"] = &Alarm{Name: "host_down", Chart: "system.up", Status: StatusCritical}
	e.notifyAt(LogEntry{Name: "http_down", Chart: "web.requests", Status: StatusWarning, When: 5}, 5)
	d := e.NotificationDiagnostics()
	if d.Suppressed != 1 || len(d.Recent) != 1 || d.Recent[0].Reason != "dependency" || d.Enqueued != 0 {
		t.Fatalf("%+v", d)
	}
	e.notifyAt(LogEntry{Name: "host_down", Chart: "system.up", Status: StatusCritical, When: 6}, 6)
	if e.NotificationDiagnostics().Enqueued != 1 {
		t.Fatal(e.NotificationDiagnostics())
	}
	e.notifyAt(LogEntry{Name: "other", Chart: "system.cpu", Status: StatusCritical, When: 7}, 7)
	if e.NotificationDiagnostics().Enqueued != 2 {
		t.Fatal(e.NotificationDiagnostics())
	}
	e.alarms["host_down|system.up"].Status = StatusWarning
	e.notifyAt(LogEntry{Name: "disk_full", Chart: "disk.space", Status: StatusCritical, When: 8}, 8)
	if e.NotificationDiagnostics().Enqueued != 3 {
		t.Fatal(e.NotificationDiagnostics())
	}
}

func TestInhibitWildcardAndEqualChart(t *testing.T) {
	e := diagnosticsEngine(t, Options{
		Inhibit: []InhibitRule{{
			Source: "external_check_status", Status: "warning", Targets: []string{"*"}, Equal: []string{"chart"},
		}},
		Notifiers: []Notifier{diagnosticNotifier{"slack", func() error { return nil }}},
	})
	e.alarms["external_check_status|check.db"] = &Alarm{Name: "external_check_status", Chart: "check.db", Status: StatusWarning}
	e.notifyAt(LogEntry{Name: "external_check_status", Chart: "check.web", Status: StatusCritical, When: 1}, 1)
	if e.NotificationDiagnostics().Enqueued != 1 {
		t.Fatal(e.NotificationDiagnostics())
	}
	e.notifyAt(LogEntry{Name: "slow_query", Chart: "check.db", Status: StatusWarning, When: 2}, 2)
	d := e.NotificationDiagnostics()
	if d.Suppressed != 1 || d.Recent[0].Reason != "dependency" {
		t.Fatalf("%+v", d)
	}
	e.notifyAt(LogEntry{Name: "slow_query", Chart: "db.query", Status: StatusWarning, When: 3}, 3)
	if e.NotificationDiagnostics().Enqueued != 2 {
		t.Fatal(e.NotificationDiagnostics())
	}
	e.notifyAt(LogEntry{Name: "slow_query", Chart: "check.db", Status: StatusClear, When: 4}, 4)
	if e.NotificationDiagnostics().Enqueued != 3 {
		t.Fatal(e.NotificationDiagnostics())
	}
}

func TestInhibitMatchesChartLabel(t *testing.T) {
	e := diagnosticsEngine(t, Options{
		Inhibit:   []InhibitRule{{Source: "parent", Targets: []string{"child"}, Equal: []string{"label:service"}}},
		Notifiers: []Notifier{diagnosticNotifier{"slack", func() error { return nil }}},
	})
	e.alarms["parent|app.a"] = &Alarm{Name: "parent", Chart: "app.a", Status: StatusCritical, Labels: map[string]string{"service": "api"}}
	e.alarms["child|app.b"] = &Alarm{Name: "child", Chart: "app.b", Labels: map[string]string{"service": "api"}}
	e.notifyAt(LogEntry{Name: "child", Chart: "app.b", Status: StatusWarning, When: 1}, 1)
	if e.NotificationDiagnostics().Suppressed != 1 || e.NotificationDiagnostics().Enqueued != 0 {
		t.Fatal(e.NotificationDiagnostics())
	}
	e.alarms["child|app.c"] = &Alarm{Name: "child", Chart: "app.c", Labels: map[string]string{"service": "db"}}
	e.notifyAt(LogEntry{Name: "child", Chart: "app.c", Status: StatusWarning, When: 2}, 2)
	if e.NotificationDiagnostics().Enqueued != 1 {
		t.Fatal(e.NotificationDiagnostics())
	}
}

func TestInhibitRulesRejectAmbiguousConfig(t *testing.T) {
	for _, rule := range []InhibitRule{
		{Source: "host_down"},
		{Source: "*", Targets: []string{"child"}},
		{Source: "host_down", Targets: []string{"*", "child"}},
		{Source: "host_down", Targets: []string{"child"}, Equal: []string{"hostname"}},
		{Source: "host_down", Status: "down", Targets: []string{"child"}},
	} {
		if _, err := New(nil, nil, Options{Inhibit: []InhibitRule{rule}}); err == nil {
			t.Fatalf("accepted %+v", rule)
		}
	}
}
