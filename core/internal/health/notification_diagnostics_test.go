package health

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type diagnosticNotifier struct {
	name string
	call func() error
}

func (n diagnosticNotifier) Name() string                           { return n.name }
func (n diagnosticNotifier) Notify(context.Context, LogEntry) error { return n.call() }

func diagnosticsEngine(t *testing.T, opt Options) *Engine {
	t.Helper()
	opt.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	e, err := New(nil, nil, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}

func TestNotificationDiagnosticsMixedHTTPResultsAndPrivacy(t *testing.T) {
	const secret = "private-webhook-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/failed" {
			w.WriteHeader(503)
			_, _ = io.WriteString(w, secret)
			return
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	e := diagnosticsEngine(t, Options{Notifiers: []Notifier{
		&WebhookNotifier{URL: srv.URL + "/ok?token=" + secret},
		&SlackNotifier{WebhookURL: srv.URL + "/failed?token=" + secret},
	}})
	var logs bytes.Buffer
	e.log = slog.New(slog.NewTextHandler(&logs, nil))
	e.startDispatch()
	e.record(LogEntry{Name: "high_ram", Chart: "system.ram", Status: StatusWarning, Info: secret, Recipient: secret}, true)
	e.Close()
	d := e.NotificationDiagnostics()
	if d.Accepted != 1 || d.Failed != 1 || d.Enqueued != 1 || d.Total != 2 || d.InFlight != nil || d.QueueSize != 0 {
		t.Fatalf("%+v", d)
	}
	if d.Recent[0].Channel != "slack" || d.Recent[0].HTTPStatus != 503 || d.Recent[0].Reason != "http_status" || d.Recent[1].Outcome != "accepted" {
		t.Fatal(d.Recent)
	}
	if d.Channels[0].Name != "slack" || d.Channels[0].Failed != 1 || d.Channels[1].Accepted != 1 {
		t.Fatal(d.Channels)
	}
	if !e.Log(0)[0].Notified || e.Notified() != 1 {
		t.Fatal("partial success must preserve legacy any-channel semantics")
	}
	b, _ := json.Marshal(d)
	for _, data := range []string{string(b), logs.String()} {
		if strings.Contains(data, secret) || strings.Contains(data, srv.URL) {
			t.Fatal("provider credential leaked")
		}
	}
	// Returned values do not alias live counters or results.
	d.Channels[0].Failed = 100
	d.Recent[0].Name = "changed"
	if next := e.NotificationDiagnostics(); next.Channels[0].Failed != 1 || next.Recent[0].Name == "changed" {
		t.Fatal("snapshot alias")
	}
}

func TestNotificationSuppressionRoutingAndRetention(t *testing.T) {
	e := diagnosticsEngine(t, Options{Notifiers: []Notifier{&memNotifier{}}, Roles: map[string][]string{"missing": {"webhook"}}})
	entry := LogEntry{Name: "a", Chart: "c", Status: StatusWarning}
	all := true
	e.ApplySilence(&all, "", 0, false)
	e.notify(entry)
	e.ApplySilence(nil, "c.a", 0, false)
	all = false
	e.ApplySilence(&all, "", 0, false)
	e.notify(entry)
	e.ApplySilence(nil, "c.a", 0, true)
	e.mu.Lock()
	e.maintUntil = time.Now().Add(time.Minute).Unix()
	e.mu.Unlock()
	e.notify(entry)
	e.mu.Lock()
	e.maintUntil = 0
	e.mu.Unlock()
	entry.Recipient = " silent "
	e.notify(entry)
	entry.Recipient = "missing"
	e.notify(entry)
	d := e.NotificationDiagnostics()
	if d.Suppressed != 4 || d.Unrouted != 1 || d.Enqueued != 0 {
		t.Fatal(d)
	}
	want := []string{"no_channel", "silent_recipient", "maintenance", "alarm_silence", "global_silence"}
	for i, reason := range want {
		if d.Recent[i].Reason != reason {
			t.Fatal(d.Recent)
		}
	}
	for i := 0; i < 600; i++ {
		e.notify(entry)
	}
	d = e.NotificationDiagnostics()
	if len(d.Recent) != 500 || d.Total != 605 || d.Unrouted != 601 || d.Recent[0].ID != 605 || d.Recent[499].ID != 106 {
		t.Fatal("retention/counters", d.Total, len(d.Recent))
	}
	empty := diagnosticsEngine(t, Options{})
	empty.notify(LogEntry{})
	if empty.NotificationDiagnostics().Unrouted != 1 {
		t.Fatal("no configured channels")
	}
}

func TestNotificationQueuePressureInFlightAndConcurrentSnapshots(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	n := diagnosticNotifier{name: "webhook", call: func() error { once.Do(func() { close(started); <-release }); return nil }}
	e := diagnosticsEngine(t, Options{Notifiers: []Notifier{n, n}})
	e.startDispatch()
	e.notify(LogEntry{Name: "first"})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("dispatcher did not start")
	}
	// Ensure a failed assertion cannot leave Close waiting on the fake sender.
	defer close(release)
	d := e.NotificationDiagnostics()
	if d.InFlight == nil || d.InFlight.Name != "first" || d.Channels[0].ConfiguredCount != 2 {
		t.Fatal(d)
	}
	d.InFlight.Name = "changed"
	if e.NotificationDiagnostics().InFlight.Name != "first" {
		t.Fatal("in flight alias")
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 70; j++ {
				e.notify(LogEntry{Name: "queued"})
				_ = e.NotificationDiagnostics()
			}
		}()
	}
	wg.Wait()
	d = e.NotificationDiagnostics()
	if d.QueueSize != 256 || d.Enqueued != 257 || d.Dropped != 24 || d.Channels[0].Attempts != 1 {
		t.Fatal(d)
	}
}

func TestNotificationErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   string
		status int
	}{
		{nil, "", 0}, {fmt.Errorf("wrapped: %w", &notificationHTTPError{429}), "http_status", 429},
		{&url.Error{URL: "https://secret", Err: context.DeadlineExceeded}, "timeout", 0},
		{context.Canceled, "canceled", 0}, {&url.Error{URL: "https://secret", Err: errors.New("connection")}, "network", 0},
		{errors.New("sensitive body"), "provider_error", 0},
	} {
		code, status := notificationError(tc.err)
		if code != tc.code || status != tc.status {
			t.Fatal(code, status)
		}
	}
	if diagnosticChannel("https://private-token") != "custom" {
		t.Fatal("unsafe channel identifier")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, "secret-response")
	}))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"?token=private", nil)
	err := doNotify(context.Background(), nil, req)
	if code, status := notificationError(err); code != "http_status" || status != 401 || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") {
		t.Fatal(err)
	}
}
