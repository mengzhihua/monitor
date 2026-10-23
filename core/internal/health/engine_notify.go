package health

import (
	"context"
	"strings"
	"time"
)

type notifyGroup struct {
	ready   time.Time
	entries []LogEntry
}

// startDispatch launches the notification dispatcher (once).
func (e *Engine) startDispatch() {
	e.startOnce.Do(func() {
		e.dispatchWG.Add(1)
		go func() {
			defer e.dispatchWG.Done()
			e.dispatch()
		}()
	})
}

// notify queues an entry for delivery. Delivery state (Notified, NotifiedAt,
// Alarm.LastNotified) is only recorded once a notifier actually succeeds.
func (e *Engine) notify(entry LogEntry) {
	e.notifyAt(entry, e.now().Unix())
}

func (e *Engine) notifyAt(entry LogEntry, at int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	// Repeat cadence is about scheduled attempts. Suppression, failed delivery
	// and a full queue must neither stop all future repeats nor retry every tick.
	if a := e.alarms[entry.Name+"|"+entry.Chart]; a != nil && a.ID == entry.AlarmID && at > a.lastNotifyAttempt {
		a.lastNotifyAttempt = at
	}
	if reason := e.suppressionReasonLocked(entry.Chart, entry.Name); reason != "" {
		e.diagnostics.Suppressed++
		e.addNotificationResultLocked(entry, "", "suppressed", reason, 0, 0)
		return
	}
	if strings.TrimSpace(entry.Recipient) != "silent" {
		if to := e.onCallRecipientLocked(time.Unix(at, 0)); to != "" {
			entry.Recipient = to
		}
	}
	if e.opt.EscalateAfter > 0 && e.opt.EscalateTo != "" && entry.Repeat && entry.Status == StatusCritical {
		if a := e.alarms[entry.Name+"|"+entry.Chart]; a != nil && at-a.LastStatusChange >= int64(e.opt.EscalateAfter/time.Second) {
			entry.Recipient = e.opt.EscalateTo
		}
	}
	if e.opt.InhibitSameChart && entry.Status == StatusWarning {
		for _, a := range e.alarms {
			if a.Chart == entry.Chart && a.Name != entry.Name && a.Status == StatusCritical {
				e.diagnostics.Suppressed++
				e.addNotificationResultLocked(entry, "", "suppressed", "inhibited", 0, 0)
				e.noteInhibitedActionLocked(&entry, at, "same_chart")
				return
			}
		}
	}
	if e.dependencyInhibitedLocked(entry) {
		e.diagnostics.Suppressed++
		e.addNotificationResultLocked(entry, "", "suppressed", "dependency", 0, 0)
		return
	}
	if e.correlationSuppressedLocked(entry) {
		e.diagnostics.Suppressed++
		e.addNotificationResultLocked(entry, "", "suppressed", "correlation", 0, 0)
		return
	}
	e.applyActionLocked(&entry, at)
	if strings.TrimSpace(entry.Recipient) == "silent" {
		e.diagnostics.Suppressed++
		e.addNotificationResultLocked(entry, "", "suppressed", "silent_recipient", 0, 0)
		return
	}
	if e.opt.GroupWait > 0 && entry.testChannel == "" {
		if e.groups == nil {
			e.groups = map[string]*notifyGroup{}
		}
		key := entry.Chart + "\n" + strings.TrimSpace(entry.Recipient)
		if g := e.groups[key]; g != nil {
			g.entries = append(g.entries, entry)
			e.diagnostics.Suppressed++
			e.addNotificationResultLocked(entry, "", "suppressed", "grouped", 0, 0)
			return
		}
		e.groups[key] = &notifyGroup{ready: time.Unix(at, 0).Add(e.opt.GroupWait), entries: []LogEntry{entry}}
		return
	}
	e.enqueueNotifyLocked(entry)
}

func (e *Engine) enqueueNotifyLocked(entry LogEntry) {
	if len(e.notifiersFor(entry.Recipient)) == 0 {
		e.diagnostics.Unrouted++
		e.addNotificationResultLocked(entry, "", "unrouted", "no_channel", 0, 0)
		return
	}
	select {
	case e.notifyCh <- entry:
		e.diagnostics.Enqueued++
	default:
		e.diagnostics.Dropped++
		e.addNotificationResultLocked(entry, "", "dropped", "queue_full", 0, 0)
		e.log.Warn("notification queue full, dropping", "alarm", entry.Name)
	}
}

func (e *Engine) flushNotifyGroups(now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for key, g := range e.groups {
		if now.Before(g.ready) || len(g.entries) == 0 {
			continue
		}
		first := g.entries[0]
		if len(g.entries) > 1 {
			names := make([]string, len(g.entries))
			for i, en := range g.entries {
				names[i] = en.Name
			}
			first.Info = strings.TrimSpace(first.Info + " also " + strings.Join(names, ", "))
		}
		delete(e.groups, key)
		e.enqueueNotifyLocked(first)
	}
}

func (e *Engine) dispatch() {
	for entry := range e.notifyCh {
		notifiers := e.notifiersFor(entry.Recipient)
		if entry.testChannel != "" {
			notifiers = nil
			for _, n := range e.opt.Notifiers {
				if diagnosticChannel(n.Name()) == entry.testChannel {
					notifiers = append(notifiers, n)
				}
			}
		}
		for _, n := range notifiers {
			channel := diagnosticChannel(n.Name())
			e.beginNotification(entry, channel)
			started := time.Now()
			cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := n.Notify(cctx, entry)
			cancel()
			code, httpStatus := notificationError(err)
			e.finishNotification(entry, channel, code, httpStatus, time.Since(started).Milliseconds())
			if err != nil {
				// Provider errors may contain webhook credentials or response bodies.
				e.log.Error("notify failed", "via", channel, "alarm", entry.Name, "reason", code, "http_status", httpStatus)
				continue
			}
			if entry.testChannel == "" {
				e.markNotified(entry)
				e.notified.Add(1)
			}
		}
	}
}

func (e *Engine) RoutingInfo() map[string]any {
	e.mu.RLock()
	defer e.mu.RUnlock()
	channels := make([]map[string]any, 0, len(e.opt.Notifiers))
	for _, n := range e.opt.Notifiers {
		channels = append(channels, map[string]any{"name": n.Name(), "configured": true})
	}
	roles := e.opt.Roles
	if roles == nil {
		roles = map[string][]string{}
	}
	return map[string]any{"roles": roles, "channels": channels}
}

func (e *Engine) notifiersFor(role string) []Notifier {
	role = strings.TrimSpace(role)
	if role == "silent" {
		return nil
	}
	names, ok := e.opt.Roles[role]
	if !ok || len(names) == 0 {
		return e.opt.Notifiers
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var out []Notifier
	for _, n := range e.opt.Notifiers {
		if want[n.Name()] {
			out = append(out, n)
		}
	}
	return out
}

// Notified returns how many notifications were delivered successfully.
func (e *Engine) Notified() int64 { return e.notified.Load() }
