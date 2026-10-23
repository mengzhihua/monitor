package health

import (
	"bufio"
	"encoding/json"
	"os"
)

// logUpdate is appended to alarm-log.jsonl when a previously written entry
// is delivered, so notification state survives restarts.
type logUpdate struct {
	Update     string `json:"update"`
	UniqueID   uint64 `json:"unique_id"`
	NotifiedAt int64  `json:"notified_at"`
}

func (e *Engine) loadLog(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var up logUpdate
		if err := json.Unmarshal(sc.Bytes(), &up); err == nil && up.Update == "notified" {
			for i := len(e.entries) - 1; i >= 0; i-- {
				if e.entries[i].UniqueID == up.UniqueID {
					e.entries[i].Notified, e.entries[i].NotifiedAt = true, up.NotifiedAt
					break
				}
			}
			continue
		}
		var le LogEntry
		if err := json.Unmarshal(sc.Bytes(), &le); err != nil {
			continue
		}
		e.entries = append(e.entries, le)
		if le.UniqueID >= e.nextLog {
			e.nextLog = le.UniqueID + 1
		}
		if le.AlarmID >= e.nextID {
			e.nextID = le.AlarmID + 1
		}
	}
	if len(e.entries) > e.opt.LogKeep {
		e.entries = e.entries[len(e.entries)-e.opt.LogKeep:]
	}
	return sc.Err()
}

// record appends to the alarm log, fires OnEvent and optionally notifies.
func (e *Engine) record(entry LogEntry, notify bool) LogEntry {
	e.mu.Lock()
	entry.UniqueID = e.nextLog
	e.nextLog++
	e.entries = append(e.entries, entry)
	if len(e.entries) > e.opt.LogKeep {
		e.entries = e.entries[len(e.entries)-e.opt.LogKeep:]
	}
	if e.logFile != nil {
		if b, err := json.Marshal(entry); err == nil {
			_, _ = e.logFile.Write(append(b, '\n'))
		}
	}
	e.mu.Unlock()
	e.log.Info("alarm", "name", entry.Name, "chart", entry.Chart, "status", entry.Status.String(),
		"old", entry.OldStatus.String(), "value", entry.Value, "units", entry.Units, "delay", entry.Delay)
	if e.opt.OnEvent != nil {
		e.opt.OnEvent(entry)
	}
	if notify {
		e.notifyAt(entry, entry.When)
	}
	return entry
}

func (e *Engine) markNotified(entry LogEntry) {
	at := e.now().Unix()
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, a := range e.alarms {
		if a.ID == entry.AlarmID && entry.When > a.LastNotified {
			a.LastNotified = entry.When
		}
	}
	for i := range e.entries {
		if e.entries[i].UniqueID == entry.UniqueID {
			e.entries[i].Notified, e.entries[i].NotifiedAt = true, at
		}
	}
	if e.logFile != nil {
		if b, err := json.Marshal(logUpdate{Update: "notified", UniqueID: entry.UniqueID, NotifiedAt: at}); err == nil {
			_, _ = e.logFile.Write(append(b, '\n'))
		}
	}
}

// Log returns entries with UniqueID > after (all when after == 0), oldest first.
func (e *Engine) Log(after uint64) []LogEntry {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]LogEntry, 0, len(e.entries))
	for _, le := range e.entries {
		if le.UniqueID > after {
			out = append(out, le)
		}
	}
	return out
}
