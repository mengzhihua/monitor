package health

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestWarningInhibitedByCriticalOnSameChart(t *testing.T) {
	e := diagnosticsEngine(t, Options{InhibitSameChart: true, Notifiers: []Notifier{diagnosticNotifier{"slack", func() error { return nil }}}})
	e.alarms["hot|system.ram"] = &Alarm{Name: "hot", Chart: "system.ram", Status: StatusCritical}
	e.notifyAt(LogEntry{Name: "warm", Chart: "system.ram", Status: StatusWarning, When: 5}, 5)
	d := e.NotificationDiagnostics()
	if d.Suppressed != 1 || len(d.Recent) != 1 || d.Recent[0].Reason != "inhibited" {
		t.Fatalf("%+v", d)
	}
	e.notifyAt(LogEntry{Name: "warm", Chart: "disk.space", Status: StatusWarning, When: 6}, 6)
	if e.NotificationDiagnostics().Enqueued != 1 {
		t.Fatal(e.NotificationDiagnostics())
	}
}

func TestGroupWaitSendsOneChartNotification(t *testing.T) {
	var mu sync.Mutex
	var infos []string
	e := diagnosticsEngine(t, Options{GroupWait: time.Second, Notifiers: []Notifier{diagnosticNotifier{"slack", func() error { return nil }}}})
	e.opt.Notifiers = []Notifier{recordingNotifier{name: "slack", infos: &infos, mu: &mu}}
	e.startDispatch()
	e.notifyAt(LogEntry{Name: "a", Chart: "disk.ops", Status: StatusWarning, When: 10}, 10)
	e.notifyAt(LogEntry{Name: "b", Chart: "disk.ops", Status: StatusWarning, When: 11}, 11)
	if e.NotificationDiagnostics().Enqueued != 0 {
		t.Fatal("grouped notifications must wait")
	}
	e.flushNotifyGroups(time.Unix(12, 0))
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(infos)
		mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(infos) != 1 || infos[0] == "" {
		t.Fatalf("infos=%v", infos)
	}
}

func TestCriticalRepeatEscalatesRecipient(t *testing.T) {
	var mu sync.Mutex
	var got []string
	e := diagnosticsEngine(t, Options{
		EscalateAfter: time.Second, EscalateTo: "oncall",
		Roles:     map[string][]string{"oncall": {"pager"}, "sysadmin": {"slack"}},
		Notifiers: []Notifier{namedNotifier{name: "pager", seen: &got, mu: &mu}, namedNotifier{name: "slack", seen: &got, mu: &mu}},
	})
	e.alarms["hot|system.cpu"] = &Alarm{ID: 7, Name: "hot", Chart: "system.cpu", Status: StatusCritical, LastStatusChange: 10}
	e.startDispatch()
	e.notifyAt(LogEntry{AlarmID: 7, Name: "hot", Chart: "system.cpu", Status: StatusCritical, Repeat: true, Recipient: "sysadmin", When: 12}, 12)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != "pager" {
		t.Fatalf("channels=%v", got)
	}
}

func TestOnCallWindowRoutesBeforeEscalation(t *testing.T) {
	inside := time.Date(2024, 1, 8, 10, 30, 0, 0, time.Local) // Monday
	window := OnCallWindow{Start: "09:00", End: "18:00", Weekdays: []string{"mon"}, To: "oncall"}
	var mu sync.Mutex
	var got []string
	e := diagnosticsEngine(t, Options{
		OnCall:    []OnCallWindow{window},
		Roles:     map[string][]string{"oncall": {"pager"}, "sysadmin": {"slack"}},
		Notifiers: []Notifier{namedNotifier{name: "pager", seen: &got, mu: &mu}, namedNotifier{name: "slack", seen: &got, mu: &mu}},
	})
	e.startDispatch()
	e.notifyAt(LogEntry{Name: "warm", Chart: "disk.space", Status: StatusWarning, Recipient: "sysadmin", When: inside.Unix()}, inside.Unix())
	waitNotify(t, &mu, &got)
	mu.Lock()
	if len(got) != 1 || got[0] != "pager" {
		t.Fatalf("inside window channels=%v", got)
	}
	got = nil
	mu.Unlock()

	outside := time.Date(2024, 1, 8, 19, 0, 0, 0, time.Local)
	e.notifyAt(LogEntry{Name: "warm", Chart: "disk.space", Status: StatusWarning, Recipient: "sysadmin", When: outside.Unix()}, outside.Unix())
	waitNotify(t, &mu, &got)
	mu.Lock()
	if len(got) != 1 || got[0] != "slack" {
		t.Fatalf("outside window channels=%v", got)
	}
	mu.Unlock()

	e.notifyAt(LogEntry{Name: "quiet", Chart: "disk.space", Status: StatusWarning, Recipient: "silent", When: inside.Unix()}, inside.Unix())
	recent := e.NotificationDiagnostics().Recent
	if len(recent) == 0 || recent[0].Reason != "silent_recipient" {
		t.Fatal(recent)
	}

	late := diagnosticsEngine(t, Options{
		OnCall: []OnCallWindow{window}, EscalateAfter: time.Second, EscalateTo: "pager",
		Roles:     map[string][]string{"oncall": {"slack"}, "pager": {"pager"}},
		Notifiers: []Notifier{namedNotifier{name: "pager", seen: &got, mu: &mu}, namedNotifier{name: "slack", seen: &got, mu: &mu}},
	})
	late.alarms["hot|system.cpu"] = &Alarm{ID: 9, Name: "hot", Chart: "system.cpu", Status: StatusCritical, LastStatusChange: inside.Unix() - 5}
	late.startDispatch()
	mu.Lock()
	got = nil
	mu.Unlock()
	late.notifyAt(LogEntry{AlarmID: 9, Name: "hot", Chart: "system.cpu", Status: StatusCritical, Repeat: true, Recipient: "sysadmin", When: inside.Unix()}, inside.Unix())
	waitNotify(t, &mu, &got)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != "pager" {
		t.Fatalf("escalated channels=%v", got)
	}
}

func TestLatestDeliveryKeepsNewestRealOutcome(t *testing.T) {
	e := diagnosticsEngine(t, Options{Notifiers: []Notifier{diagnosticNotifier{"slack", func() error { return nil }}}})
	e.notifyAt(LogEntry{Name: "hot", Chart: "system.cpu", Status: StatusWarning, When: 1, testChannel: "slack"}, 1)
	if _, ok := e.LatestDelivery("system.cpu", "hot"); ok {
		t.Fatal("test notifications are not delivery")
	}
	e.mu.Lock()
	e.addNotificationResultLocked(LogEntry{Name: "hot", Chart: "system.cpu", Status: StatusWarning}, "slack", "failed", "timeout", 0, 1)
	e.addNotificationResultLocked(LogEntry{Name: "hot", Chart: "system.cpu", Status: StatusCritical}, "slack", "accepted", "", 0, 2)
	e.mu.Unlock()
	got, ok := e.LatestDelivery("system.cpu", "hot")
	if !ok || got.Outcome != "accepted" || got.Channel != "slack" {
		t.Fatalf("%+v %v", got, ok)
	}
}

func waitNotify(t *testing.T, mu *sync.Mutex, got *[]string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(*got)
		mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("notification was not delivered")
}

type recordingNotifier struct {
	name  string
	infos *[]string
	mu    *sync.Mutex
}

func (n recordingNotifier) Name() string { return n.name }
func (n recordingNotifier) Notify(_ context.Context, e LogEntry) error {
	n.mu.Lock()
	*n.infos = append(*n.infos, e.Info)
	n.mu.Unlock()
	return nil
}

type namedNotifier struct {
	name string
	seen *[]string
	mu   *sync.Mutex
}

func (n namedNotifier) Name() string { return n.name }
func (n namedNotifier) Notify(context.Context, LogEntry) error {
	n.mu.Lock()
	*n.seen = append(*n.seen, n.name)
	n.mu.Unlock()
	return nil
}
