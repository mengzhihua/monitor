package collect

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func (l *logsCollector) query(q LogQuery) []LogRow {
	if q.Source == "journal" || q.Source == "journald" || (q.Source == "" && l.followOn) {
		if rows := l.journalRows(q); len(rows) > 0 || q.Source == "journal" || q.Source == "journald" {
			return rows
		}
	}
	return QueryLogs(q)
}

func (l *logsCollector) journalRows(q LogQuery) []LogRow {
	filtered := q.Unit != "" || q.Priority != "" || q.Boot != "" || q.Cursor != ""
	if l.followOn && !filtered {
		l.mu.Lock()
		rows := append([]LogRow(nil), l.buf...)
		l.mu.Unlock()
		out := make([]LogRow, 0, len(rows))
		for _, r := range rows {
			if acceptLog(q, r) {
				out = append(out, r)
			}
		}
		if len(out) > q.Limit && q.Limit > 0 {
			out = out[len(out)-q.Limit:]
		}
		if len(out) > 0 {
			return out
		}
	}
	return queryJournal(q)
}

func (l *logsCollector) startFollow() {
	if !l.followEnabled() {
		return
	}
	if startSDJournal(l) {
		return
	}
	if _, err := exec.LookPath("journalctl"); err != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "journalctl", "-f", "-o", "json", "--no-pager", "-n", "0")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return
	}
	l.cancel = cancel
	l.followOn = true
	go func() {
		defer cancel()
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			for _, r := range parseJournalJSON(sc.Text(), LogQuery{Limit: 1}) {
				l.mu.Lock()
				l.buf = append(l.buf, r)
				if len(l.buf) > 2000 {
					l.buf = l.buf[len(l.buf)-2000:]
				}
				l.pending++
				if l.pendingSev == nil {
					l.pendingSev = map[string]float64{}
				}
				l.pendingSev[normalizePri(r.Priority)]++
				l.mu.Unlock()
			}
		}
		_ = cmd.Wait()
	}()
}

func journalArgs(q LogQuery) []string {
	n := q.Limit
	if n <= 0 {
		n = 200
	}
	args := []string{"-o", "json", "--no-pager", "-n", strconv.Itoa(n)}
	if q.Unit != "" {
		args = append(args, "-u", q.Unit)
	}
	if q.Priority != "" {
		args = append(args, "-p", normalizePri(q.Priority))
	}
	switch q.Boot {
	case "":
	case "0", "current":
		args = append(args, "-b")
	default:
		args = append(args, "-b", q.Boot)
	}
	if q.Cursor != "" {
		args = append(args, "--after-cursor", q.Cursor)
	}
	if q.After > 0 {
		args = append(args, "--since", time.Unix(q.After, 0).UTC().Format("2006-01-02 15:04:05"))
	}
	if q.Before > 0 {
		args = append(args, "--until", time.Unix(q.Before, 0).UTC().Format("2006-01-02 15:04:05"))
	}
	if q.Query != "" {
		args = append(args, "-g", q.Query)
	}
	return args
}

func parseJournalJSON(s string, q LogQuery) []LogRow {
	var rows []LogRow
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		row := LogRow{
			Priority: journalPri(fmt.Sprint(m["PRIORITY"])),
			Unit:     firstString(m, "_SYSTEMD_UNIT", "SYSLOG_IDENTIFIER", "_COMM"),
			PID:      firstString(m, "_PID", "SYSLOG_PID"),
			Message:  firstString(m, "MESSAGE"),
			Cursor:   firstString(m, "__CURSOR"),
			Boot:     firstString(m, "_BOOT_ID"),
		}
		if ts := firstString(m, "__REALTIME_TIMESTAMP"); ts != "" {
			if n, err := strconv.ParseInt(ts, 10, 64); err == nil {
				row.Time = n / 1e6
			}
		}
		if !acceptLog(q, row) {
			continue
		}
		rows = append(rows, row)
	}
	if q.Limit > 0 && len(rows) > q.Limit {
		rows = rows[len(rows)-q.Limit:]
	}
	return rows
}

func acceptLog(q LogQuery, row LogRow) bool {
	if q.Unit != "" {
		u := strings.ToLower(row.Unit)
		want := strings.ToLower(q.Unit)
		if u != want && !strings.Contains(u, want) {
			return false
		}
	}
	if q.Priority != "" && priRank(row.Priority) > priRank(q.Priority) {
		return false
	}
	if q.Boot != "" && q.Boot != "0" && q.Boot != "current" && row.Boot != "" && !strings.EqualFold(row.Boot, q.Boot) {
		return false
	}
	if q.After > 0 && row.Time > 0 && row.Time < q.After {
		return false
	}
	if q.Before > 0 && row.Time > 0 && row.Time > q.Before {
		return false
	}
	if q.Query != "" && !strings.Contains(strings.ToLower(row.Message+" "+row.Unit), strings.ToLower(q.Query)) {
		return false
	}
	return true
}

func priRank(s string) int {
	switch normalizePri(s) {
	case "emerg":
		return 0
	case "alert":
		return 1
	case "crit":
		return 2
	case "err":
		return 3
	case "warning":
		return 4
	case "notice":
		return 5
	case "debug":
		return 7
	default:
		return 6
	}
}

func cursorNewer(row, after string) bool {
	if after == "" || row == "" {
		return true
	}
	a, ea := strconv.ParseInt(row, 10, 64)
	b, eb := strconv.ParseInt(after, 10, 64)
	if ea == nil && eb == nil {
		return a > b
	}
	return row > after
}

// eventXPath builds a wevtutil /q filter. An explicit XPath wins.
func eventXPath(q LogQuery) string {
	if q.XPath != "" {
		return q.XPath
	}
	var parts []string
	if id, err := strconv.ParseInt(q.Cursor, 10, 64); err == nil && q.Cursor != "" {
		parts = append(parts, fmt.Sprintf("EventRecordID>%d", id))
	}
	if q.After > 0 {
		parts = append(parts, fmt.Sprintf("TimeCreated[@SystemTime>='%s']", time.Unix(q.After, 0).UTC().Format(time.RFC3339)))
	}
	if q.Before > 0 {
		parts = append(parts, fmt.Sprintf("TimeCreated[@SystemTime<='%s']", time.Unix(q.Before, 0).UTC().Format(time.RFC3339)))
	}
	if len(parts) == 0 {
		return ""
	}
	return "*[System[" + strings.Join(parts, " and ") + "]]"
}

func queryUnified(q LogQuery) []LogRow {
	if runtime.GOOS != "darwin" {
		return nil
	}
	if _, err := exec.LookPath("log"); err != nil {
		return nil
	}
	n := q.Limit
	if n <= 0 {
		n = 200
	}
	args := []string{"show", "--style", "json", "--last", "15m"}
	if q.After > 0 {
		args = []string{"show", "--style", "json", "--start", time.Unix(q.After, 0).Format("2006-01-02 15:04:05")}
	}
	if q.Before > 0 {
		args = append(args, "--end", time.Unix(q.Before, 0).Format("2006-01-02 15:04:05"))
	}
	if q.Unit != "" {
		args = append(args, "--predicate", fmt.Sprintf(`processImagePath CONTAINS "%s" OR subsystem CONTAINS "%s"`, q.Unit, q.Unit))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "log", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	rows := parseLogShow(string(out), q)
	if n > 0 && len(rows) > n {
		rows = rows[len(rows)-n:]
	}
	return rows
}

// Decode only the fields used by LogRow. Unified logging carries extensive
// nested metadata; generic maps allocate and decode all of it on every sample.
type unifiedLogRecord struct {
	Timestamp         string          `json:"timestamp"`
	EventMessage      string          `json:"eventMessage"`
	Message           string          `json:"message"`
	Subsystem         string          `json:"subsystem"`
	ProcessImagePath  string          `json:"processImagePath"`
	SenderImagePath   string          `json:"senderImagePath"`
	ProcessID         json.RawMessage `json:"processID"`
	ProcessIdentifier json.RawMessage `json:"processIdentifier"`
	MessageType       string          `json:"messageType"`
	Type              string          `json:"type"`
}

func unifiedPID(value json.RawMessage) string {
	if len(value) == 0 || string(value) == "null" {
		return ""
	}
	if value[0] == '"' {
		var text string
		_ = json.Unmarshal(value, &text)
		return text
	}
	return string(value)
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func parseLogShow(s string, q LogQuery) []LogRow {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var objs []unifiedLogRecord
	if strings.HasPrefix(s, "[") {
		if err := json.Unmarshal([]byte(s), &objs); err != nil {
			return nil
		}
	} else {
		sc := bufio.NewScanner(strings.NewReader(s))
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || line == "[" || line == "]" {
				continue
			}
			line = strings.TrimSuffix(line, ",")
			var m unifiedLogRecord
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				continue
			}
			objs = append(objs, m)
		}
	}
	var rows []LogRow
	for _, m := range objs {
		row := LogRow{
			Message:  firstNonempty(m.EventMessage, m.Message),
			Unit:     firstNonempty(m.Subsystem, m.ProcessImagePath, m.SenderImagePath),
			PID:      firstNonempty(unifiedPID(m.ProcessID), unifiedPID(m.ProcessIdentifier)),
			Priority: unifiedPri(firstNonempty(m.MessageType, m.Type)),
		}
		if ts := m.Timestamp; ts != "" {
			if t, err := time.Parse("2006-01-02 15:04:05.000000-0700", ts); err == nil {
				row.Time = t.Unix()
			} else if t, err := time.Parse(time.RFC3339, ts); err == nil {
				row.Time = t.Unix()
			}
		}
		if !acceptLog(q, row) {
			continue
		}
		rows = append(rows, row)
	}
	return rows
}

func unifiedPri(s string) string {
	switch strings.ToLower(s) {
	case "fault", "error":
		return "err"
	case "default", "info":
		return "info"
	case "debug":
		return "debug"
	default:
		return normalizePri(s)
	}
}
