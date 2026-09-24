package health

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"time"
)

const notificationRetention = 500

// NotificationResult intentionally excludes recipient addresses, arbitrary
// provider errors, response bodies and alarm Info. It is a terminal outcome,
// not a promise that a human has received the notification.
type NotificationResult struct {
	ID         uint64 `json:"id"`
	EventID    uint64 `json:"event_id"`
	At         int64  `json:"at"`
	Name       string `json:"name"`
	Chart      string `json:"chart"`
	Severity   Status `json:"severity"`
	Repeat     bool   `json:"repeat"`
	Channel    string `json:"channel"`
	Outcome    string `json:"outcome"`
	Reason     string `json:"reason"`
	HTTPStatus int    `json:"http_status,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	Test       bool   `json:"test,omitempty"`
}

type NotificationChannel struct {
	Name            string `json:"name"`
	ConfiguredCount int    `json:"configured_count"`
	Attempts        uint64 `json:"attempts"`
	Accepted        uint64 `json:"accepted"`
	Failed          uint64 `json:"failed"`
	LastAttempt     int64  `json:"last_attempt"`
	LastAccepted    int64  `json:"last_accepted"`
	LastFailed      int64  `json:"last_failed"`
}

// NotificationSnapshot describes this process's local engine only. Recent is
// bounded independently of lifetime counters. It is never restored from the
// persistent alarm log, whose Notified flag means any channel succeeded.
type NotificationSnapshot struct {
	Available  bool                  `json:"available"`
	Scope      string                `json:"scope"`
	Hostname   string                `json:"hostname"`
	Since      int64                 `json:"since"`
	Now        int64                 `json:"now"`
	Enabled    bool                  `json:"enabled"`
	Closed     bool                  `json:"closed"`
	QueueSize  int                   `json:"queue_size"`
	QueueLimit int                   `json:"queue_limit"`
	Retention  int                   `json:"retention"`
	Enqueued   uint64                `json:"enqueued"`
	Suppressed uint64                `json:"suppressed"`
	Unrouted   uint64                `json:"unrouted"`
	Dropped    uint64                `json:"dropped"`
	Accepted   uint64                `json:"accepted"`
	Failed     uint64                `json:"failed"`
	Total      uint64                `json:"total"`
	InFlight   *NotificationResult   `json:"in_flight"`
	Channels   []NotificationChannel `json:"channels"`
	Recent     []NotificationResult  `json:"recent"`
}

// Names are internal channel types, not arbitrary URLs or custom identifiers.
func diagnosticChannel(name string) string {
	switch name {
	case "webhook", "slack", "email", "dingtalk", "wecom", "feishu", "telegram", "discord", "pagerduty", "push", "apns", "fcm", "huawei", "xiaomi", "sms", "ntfy", "gotify", "bark":
		return name
	default:
		return "custom"
	}
}

func (e *Engine) initNotificationDiagnostics() {
	d := &e.diagnostics
	*d = NotificationSnapshot{Available: true, Scope: "local", Hostname: e.opt.Hostname,
		Since: e.now().Unix(), Retention: notificationRetention, QueueLimit: cap(e.notifyCh),
		Channels: []NotificationChannel{}, Recent: []NotificationResult{}}
	counts := map[string]int{}
	for _, n := range e.opt.Notifiers {
		counts[diagnosticChannel(n.Name())]++
	}
	for name, count := range counts {
		d.Channels = append(d.Channels, NotificationChannel{Name: name, ConfiguredCount: count})
	}
	sort.Slice(d.Channels, func(i, j int) bool { return d.Channels[i].Name < d.Channels[j].Name })
}

func (e *Engine) NotificationDiagnostics() NotificationSnapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	d := e.diagnostics
	d.Now, d.Enabled, d.Closed, d.QueueSize = e.now().Unix(), e.enabled, e.closed, len(e.notifyCh)
	d.Channels = append([]NotificationChannel{}, d.Channels...)
	d.Recent = make([]NotificationResult, len(e.diagnostics.Recent))
	for i, r := range e.diagnostics.Recent {
		d.Recent[len(d.Recent)-1-i] = r
	}
	if d.InFlight != nil {
		cp := *d.InFlight
		d.InFlight = &cp
	}
	return d
}

func notificationResult(entry LogEntry, channel, outcome, reason string, at int64) NotificationResult {
	return NotificationResult{EventID: entry.UniqueID, At: at, Name: entry.Name, Chart: entry.Chart,
		Severity: entry.Status, Repeat: entry.Repeat, Channel: channel, Outcome: outcome, Reason: reason, Test: entry.testChannel != ""}
}

func (e *Engine) addNotificationResultLocked(entry LogEntry, channel, outcome, reason string, status int, duration int64) {
	d := &e.diagnostics
	d.Total++
	r := notificationResult(entry, channel, outcome, reason, e.now().Unix())
	r.ID, r.HTTPStatus, r.DurationMS = d.Total, status, duration
	if len(d.Recent) == notificationRetention {
		copy(d.Recent, d.Recent[1:])
		d.Recent = d.Recent[:notificationRetention-1]
	}
	d.Recent = append(d.Recent, r)
}

func (e *Engine) beginNotification(entry LogEntry, channel string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	at := e.now().Unix()
	r := notificationResult(entry, channel, "sending", "", at)
	e.diagnostics.InFlight = &r
	for i := range e.diagnostics.Channels {
		c := &e.diagnostics.Channels[i]
		if c.Name == channel {
			c.Attempts++
			c.LastAttempt = at
		}
	}
}

func (e *Engine) finishNotification(entry LogEntry, channel, code string, status int, duration int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	d := &e.diagnostics
	d.InFlight = nil
	outcome := "accepted"
	if code == "" {
		d.Accepted++
	} else {
		d.Failed++
		outcome = "failed"
	}
	for i := range d.Channels {
		c := &d.Channels[i]
		if c.Name != channel {
			continue
		}
		if code == "" {
			c.Accepted++
			c.LastAccepted = e.now().Unix()
		} else {
			c.Failed++
			c.LastFailed = e.now().Unix()
		}
	}
	e.addNotificationResultLocked(entry, channel, outcome, code, status, duration)
}

// Only the status is retained. Error() must never contain the request URL or
// provider's response body, both of which can contain credentials.
type notificationHTTPError struct{ status int }

func (e *notificationHTTPError) Error() string { return fmt.Sprintf("notification HTTP %d", e.status) }

func notificationError(err error) (string, int) {
	if err == nil {
		return "", 0
	}
	var httpErr *notificationHTTPError
	if errors.As(err, &httpErr) {
		return "http_status", httpErr.status
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout", 0
	}
	if errors.Is(err, context.Canceled) {
		return "canceled", 0
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "timeout", 0
		}
		return "network", 0
	}
	return "provider_error", 0
}

var (
	ErrNotificationChannel     = errors.New("notification channel is not configured")
	ErrNotificationTestLimit   = errors.New("notification test limited to once per 30 seconds")
	ErrNotificationUnavailable = errors.New("notification queue is unavailable")
)

// QueueNotificationTest uses only server-side destinations and a fixed message.
// An explicit test bypasses silence/maintenance and role routing, but never
// creates, resolves or changes a real alarm. All sends use the bounded dispatcher.
func (e *Engine) QueueNotificationTest(channel string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return ErrNotificationUnavailable
	}
	found := false
	for _, n := range e.opt.Notifiers {
		if diagnosticChannel(n.Name()) == channel {
			found = true
			break
		}
	}
	if !found {
		return ErrNotificationChannel
	}
	if !e.lastNotificationTest.IsZero() && time.Since(e.lastNotificationTest) < 30*time.Second {
		return ErrNotificationTestLimit
	}
	entry := LogEntry{Name: "Monitor 通知测试", Chart: "monitor.notification_test", Hostname: e.opt.Hostname,
		When: e.now().Unix(), Status: StatusClear, OldStatus: StatusClear,
		Info: "管理员手动发送的通道测试消息，不代表真实告警或恢复。", testChannel: channel}
	select {
	case e.notifyCh <- entry:
		e.lastNotificationTest = time.Now()
		e.diagnostics.Enqueued++
		return nil
	default:
		return ErrNotificationUnavailable
	}
}
