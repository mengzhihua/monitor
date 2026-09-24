package health

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func pushFixture(kind, endpoint string) Notifier {
	switch kind {
	case "ntfy":
		return &NtfyNotifier{URL: endpoint, Topic: "private-topic", Token: "private-token"}
	case "gotify":
		return &GotifyNotifier{URL: endpoint, Token: "private-token"}
	default:
		return &BarkNotifier{URL: endpoint, DeviceKey: "private-key", Group: "运维"}
	}
}
func pushAcknowledgement(kind string) string {
	switch kind {
	case "ntfy":
		return `{"id":"abc123","event":"message","topic":"private-topic"}`
	case "gotify":
		return `{"id":123}`
	default:
		return `{"code":200}`
	}
}

func TestPushServicesPayloadAuthAndSeverity(t *testing.T) {
	for _, kind := range []string{"ntfy", "gotify", "bark"} {
		for _, status := range []Status{StatusWarning, StatusCritical, StatusClear} {
			t.Run(kind+status.String(), func(t *testing.T) {
				sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if r.Method != "POST" || r.URL.Path != "/proxy/publish" || r.URL.RawQuery != "" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
						t.Error("invalid request")
					}
					if !strings.Contains(fmt.Sprint(body["title"]), "中文告警") || !strings.Contains(fmt.Sprint(body["title"]), status.String()) {
						t.Error("title missing alarm or severity")
					}
					key := "message"
					if kind == "bark" {
						key = "body"
					}
					if !strings.Contains(fmt.Sprint(body[key]), "检查系统") || !strings.Contains(fmt.Sprint(body[key]), "system.ram") {
						t.Error("alarm details missing")
					}
					switch kind {
					case "ntfy":
						priorities := map[Status]float64{StatusWarning: 4, StatusCritical: 5, StatusClear: 3}
						if r.Header.Get("Authorization") != "Bearer private-token" || body["topic"] != "private-topic" || body["priority"] != priorities[status] {
							t.Error("ntfy auth/priority/topic")
						}
					case "gotify":
						priorities := map[Status]float64{StatusWarning: 5, StatusCritical: 8, StatusClear: 2}
						if r.Header.Get("X-Gotify-Key") != "private-token" || body["priority"] != priorities[status] {
							t.Error("gotify auth/priority")
						}
					case "bark":
						if body["device_key"] != "private-key" || body["group"] != "运维" {
							t.Error("bark key/group")
						}
					}
					fmt.Fprint(w, pushAcknowledgement(kind))
				}))
				defer sink.Close()
				if err := pushFixture(kind, sink.URL+"/proxy/publish").Notify(context.Background(), LogEntry{Name: "中文告警", Hostname: "测试主机", Chart: "system.ram", Status: status, Info: "检查系统"}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestPushServicesRejectInvalidAndFailedResponses(t *testing.T) {
	for _, kind := range []string{"ntfy", "gotify", "bark"} {
		for _, body := range []string{"", `{}`, `null`, `<html>login</html>`, `{"code":400,"error":"private-token"}`, strings.Repeat("x", 65537), pushAcknowledgement(kind) + `{}`} {
			t.Run(kind, func(t *testing.T) {
				sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
				defer sink.Close()
				err := pushFixture(kind, sink.URL).Notify(context.Background(), LogEntry{})
				if err == nil {
					t.Fatal("invalid response reported accepted")
				}
				if strings.Contains(err.Error(), "private-token") {
					t.Fatal("private response leaked")
				}
			})
		}
		for _, status := range []int{401, 429, 503} {
			sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); fmt.Fprint(w, "private-token") }))
			err := pushFixture(kind, sink.URL).Notify(context.Background(), LogEntry{})
			reason, code := notificationError(err)
			sink.Close()
			if reason != "http_status" || code != status {
				t.Fatal(kind, reason, code)
			}
		}
	}
	// A valid acknowledgement for another ntfy topic must not be accepted.
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"abc","event":"message","topic":"wrong"}`)
	}))
	defer sink.Close()
	if pushFixture("ntfy", sink.URL).Notify(context.Background(), LogEntry{}) == nil {
		t.Fatal("wrong topic accepted")
	}
}

func TestPushServicesDoNotFollowRedirects(t *testing.T) {
	var leaked atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer destination.Close()
	for _, kind := range []string{"ntfy", "gotify", "bark"} {
		source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
		}))
		err := pushFixture(kind, source.URL).Notify(context.Background(), LogEntry{})
		source.Close()
		reason, status := notificationError(err)
		if reason != "http_status" || status != 307 {
			t.Fatal(kind, err)
		}
	}
	if leaked.Load() != 0 {
		t.Fatal("credentials were sent to redirect target")
	}
}

func TestPushServicesCancellationValidationAndRouting(t *testing.T) {
	for _, kind := range []string{"ntfy", "gotify", "bark"} {
		sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(100 * time.Millisecond)
			fmt.Fprint(w, pushAcknowledgement(kind))
		}))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		err := pushFixture(kind, sink.URL).Notify(ctx, LogEntry{})
		cancel()
		sink.Close()
		reason, _ := notificationError(err)
		if reason != "timeout" {
			t.Fatal(kind, reason)
		}
		for _, url := range []string{"", "file:///tmp/private-token", "https://user:private-token@example.com", "https://example.com?token=private-token", "https://example.com#private-token"} {
			err := pushFixture(kind, url).Notify(context.Background(), LogEntry{})
			if err == nil || strings.Contains(err.Error(), "private-token") {
				t.Fatal("unsafe URL accepted or leaked")
			}
		}
	}
	for _, n := range []interface{ Validate() error }{
		&NtfyNotifier{URL: "https://example.com", Topic: "bad/topic"}, &NtfyNotifier{URL: "https://example.com", Topic: strings.Repeat("a", 65)},
		&GotifyNotifier{URL: "https://example.com"}, &GotifyNotifier{URL: "https://example.com", Token: "bad\r\nheader"}, &BarkNotifier{URL: "https://example.com"},
	} {
		if n.Validate() == nil {
			t.Fatal("invalid configuration")
		}
	}
	calls := map[string]int{}
	e := diagnosticsEngine(t, Options{Roles: map[string][]string{"phone": {"ntfy", "bark"}}, Notifiers: []Notifier{
		diagnosticNotifier{"ntfy", func() error { calls["ntfy"]++; return nil }}, diagnosticNotifier{"gotify", func() error { calls["gotify"]++; return nil }}, diagnosticNotifier{"bark", func() error { calls["bark"]++; return nil }},
	}})
	e.notify(LogEntry{Name: "routing", Recipient: "phone", Status: StatusWarning})
	if err := e.QueueNotificationTest("gotify"); err != nil {
		t.Fatal(err)
	}
	e.startDispatch()
	e.Close()
	d := e.NotificationDiagnostics()
	if calls["ntfy"] != 1 || calls["bark"] != 1 || calls["gotify"] != 1 || d.Accepted != 3 || len(d.Channels) != 3 {
		t.Fatal(calls, d)
	}
	for _, c := range d.Channels {
		if c.Name == "custom" {
			t.Fatal("new channel collapsed to custom")
		}
	}
	if !d.Recent[0].Test || d.Recent[0].Channel != "gotify" {
		t.Fatal(d.Recent)
	}
}
