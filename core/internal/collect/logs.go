package collect

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// logsConfig is collectors.modules.logs (journald / Event Log / files).
type logsConfig struct {
	Files []string `yaml:"files"`
	Top   int      `yaml:"top"` // max rows returned by the logs function
}

type logsCollector struct {
	cfg      logsConfig
	lastUnix int64
	seenFile map[string]int64 // path → size
}

func init() {
	Register("logs", func() Collector { return &logsCollector{} })
}

func (l *logsCollector) Name() string { return "logs" }

func (l *logsCollector) Configure(decode func(v any) error) error {
	if err := decode(&l.cfg); err != nil {
		return err
	}
	if l.cfg.Top <= 0 {
		l.cfg.Top = 200
	}
	return nil
}

func (l *logsCollector) Init(reg *registry.Registry) error {
	if l.cfg.Top <= 0 {
		if err := l.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if !logsAvailable(l.cfg.Files) {
		return fmt.Errorf("no journald, event log or readable log files")
	}
	l.seenFile = map[string]int64{}
	l.lastUnix = time.Now().Unix()
	for _, c := range []*registry.Chart{
		{ID: "logs.written", Title: "Log entries written", Units: "entries/s", Priority: 900, Type: registry.Area,
			Dimensions: []*registry.Dimension{{ID: "written"}}},
		{ID: "logs.severity", Title: "Log entries by severity", Units: "entries/s", Type: registry.Stacked, Priority: 910,
			Dimensions: []*registry.Dimension{{ID: "emerg"}, {ID: "alert"}, {ID: "crit"}, {ID: "err"}, {ID: "warning"}, {ID: "notice"}, {ID: "info"}, {ID: "debug"}}},
	} {
		c.Family, c.Plugin, c.Module = "logs", "logs", "logs"
		reg.AddChart(c)
	}
	return nil
}

func logsAvailable(files []string) bool {
	if _, err := exec.LookPath("journalctl"); err == nil {
		return true
	}
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("wevtutil"); err == nil {
			return true
		}
	}
	for _, f := range files {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	for _, f := range defaultLogFiles() {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	return false
}

func defaultLogFiles() []string {
	return []string{"/var/log/syslog", "/var/log/messages", "/var/log/system.log"}
}

func (l *logsCollector) Collect(_ context.Context, reg *registry.Registry, now time.Time) error {
	since := l.lastUnix
	if since == 0 {
		since = now.Unix() - 2
	}
	l.lastUnix = now.Unix()
	rows := QueryLogs(LogQuery{After: since, Before: now.Unix(), Limit: 500, Files: l.files()})
	sev := map[string]float64{"emerg": 0, "alert": 0, "crit": 0, "err": 0, "warning": 0, "notice": 0, "info": 0, "debug": 0}
	for _, r := range rows {
		k := normalizePri(r.Priority)
		sev[k]++
	}
	_ = reg.Collect("logs.written", now, map[string]float64{"written": float64(len(rows))})
	_ = reg.Collect("logs.severity", now, sev)
	return nil
}

func (l *logsCollector) files() []string {
	if len(l.cfg.Files) > 0 {
		return l.cfg.Files
	}
	return defaultLogFiles()
}

func (l *logsCollector) Functions() []Function {
	return []Function{{
		Name:    "logs",
		Help:    "Recent system logs (journald, Windows Event Log, or configured files)",
		Timeout: 10,
		Run: func(_ context.Context, args map[string]string) (any, error) {
			q := LogQuery{Limit: l.cfg.Top, Files: l.files(), Query: args["query"], Source: args["source"]}
			if n, _ := strconv.Atoi(args["limit"]); n > 0 {
				q.Limit = n
			}
			if n, _ := strconv.ParseInt(args["after"], 10, 64); n != 0 {
				q.After = n
			}
			if n, _ := strconv.ParseInt(args["before"], 10, 64); n != 0 {
				q.Before = n
			}
			if q.Limit > 1000 {
				q.Limit = 1000
			}
			rows := QueryLogs(q)
			tab := Table{Columns: []string{"time", "priority", "unit", "pid", "message"}, Total: len(rows), Rows: make([]any, len(rows))}
			for i, r := range rows {
				tab.Rows[i] = r
			}
			return tab, nil
		},
	}}
}

// LogQuery is the on-demand logs function / GET /api/v1/logs filter.
type LogQuery struct {
	Source string
	Query  string
	After  int64
	Before int64
	Limit  int
	Files  []string
}

// LogRow is one journal / event / file line.
type LogRow struct {
	Time     int64  `json:"time"`
	Priority string `json:"priority"`
	Unit     string `json:"unit"`
	PID      string `json:"pid"`
	Message  string `json:"message"`
}

func QueryLogs(q LogQuery) []LogRow {
	if q.Limit <= 0 {
		q.Limit = 200
	}
	if q.Source == "file" || q.Source == "files" {
		return queryFileLogs(q)
	}
	if q.Source == "eventlog" || q.Source == "windows" {
		return queryEventLog(q)
	}
	if rows := queryJournal(q); len(rows) > 0 || q.Source == "journal" || q.Source == "journald" {
		return rows
	}
	if runtime.GOOS == "windows" {
		if rows := queryEventLog(q); len(rows) > 0 {
			return rows
		}
	}
	return queryFileLogs(q)
}

func queryJournal(q LogQuery) []LogRow {
	if _, err := exec.LookPath("journalctl"); err != nil {
		return nil
	}
	args := []string{"-o", "json", "-n", strconv.Itoa(q.Limit), "--no-pager"}
	if q.After > 0 {
		args = append(args, "--since", time.Unix(q.After, 0).UTC().Format("2006-01-02 15:04:05"))
	}
	if q.Before > 0 {
		args = append(args, "--until", time.Unix(q.Before, 0).UTC().Format("2006-01-02 15:04:05"))
	}
	if q.Query != "" {
		args = append(args, "-g", q.Query)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var rows []LogRow
	sc := bufio.NewScanner(strings.NewReader(string(out)))
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
			Unit:     firstString(m, "SYSLOG_IDENTIFIER", "_SYSTEMD_UNIT", "_COMM"),
			PID:      firstString(m, "_PID", "SYSLOG_PID"),
			Message:  firstString(m, "MESSAGE"),
		}
		if ts := fmt.Sprint(m["__REALTIME_TIMESTAMP"]); ts != "" && ts != "<nil>" {
			if n, err := strconv.ParseInt(ts, 10, 64); err == nil {
				row.Time = n / 1e6
			}
		}
		if q.Query != "" && !strings.Contains(strings.ToLower(row.Message+" "+row.Unit), strings.ToLower(q.Query)) {
			continue
		}
		rows = append(rows, row)
	}
	if len(rows) > q.Limit {
		rows = rows[len(rows)-q.Limit:]
	}
	return rows
}

func queryEventLog(q LogQuery) []LogRow {
	if runtime.GOOS != "windows" {
		return nil
	}
	if _, err := exec.LookPath("wevtutil"); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "wevtutil", "qe", "System", "/c:"+strconv.Itoa(q.Limit), "/rd:true", "/f:text")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parseEventLogText(string(out), q)
}

func parseEventLogText(s string, q LogQuery) []LogRow {
	var rows []LogRow
	var cur LogRow
	flush := func() {
		if cur.Message == "" && cur.Unit == "" {
			return
		}
		if q.Query != "" && !strings.Contains(strings.ToLower(cur.Message+" "+cur.Unit), strings.ToLower(q.Query)) {
			return
		}
		rows = append(rows, cur)
		cur = LogRow{}
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "Event["), line == "":
			flush()
		case strings.HasPrefix(line, "  Log:"), strings.HasPrefix(line, "Log Name:"):
			cur.Unit = strings.TrimSpace(line[strings.Index(line, ":")+1:])
		case strings.HasPrefix(line, "  Source:"), strings.HasPrefix(line, "Source:"):
			if cur.Unit == "" {
				cur.Unit = strings.TrimSpace(line[strings.Index(line, ":")+1:])
			}
		case strings.HasPrefix(line, "  Level:"), strings.HasPrefix(line, "Level:"):
			cur.Priority = normalizePri(strings.TrimSpace(line[strings.Index(line, ":")+1:]))
		case strings.HasPrefix(line, "  Date:"), strings.HasPrefix(line, "Date:"):
			t, err := time.Parse("2006-01-02T15:04:05.000", strings.TrimSpace(line[strings.Index(line, ":")+1:]))
			if err == nil {
				cur.Time = t.Unix()
			}
		case strings.HasPrefix(line, "  Description:"), strings.HasPrefix(line, "Description:"):
			cur.Message = strings.TrimSpace(line[strings.Index(line, ":")+1:])
		default:
			if strings.HasPrefix(line, "  ") && cur.Message != "" {
				cur.Message += " " + strings.TrimSpace(line)
			}
		}
	}
	flush()
	if len(rows) > q.Limit {
		rows = rows[:q.Limit]
	}
	return rows
}

func queryFileLogs(q LogQuery) []LogRow {
	files := q.Files
	if len(files) == 0 {
		files = defaultLogFiles()
	}
	var rows []LogRow
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		st, _ := f.Stat()
		const maxTail = 512 << 10
		if st != nil && st.Size() > maxTail {
			_, _ = f.Seek(st.Size()-maxTail, 0)
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if q.Query != "" && !strings.Contains(strings.ToLower(line), strings.ToLower(q.Query)) {
				continue
			}
			rows = append(rows, parseSyslogLine(line, filepath.Base(path)))
		}
		_ = f.Close()
	}
	if len(rows) > q.Limit {
		rows = rows[len(rows)-q.Limit:]
	}
	return rows
}

func parseSyslogLine(line, unit string) LogRow {
	row := LogRow{Message: line, Unit: unit, Priority: "info", Time: time.Now().Unix()}
	// RFC3164: "Jan  2 15:04:05 host tag[pid]: msg"
	if len(line) > 16 {
		if t, err := time.Parse("Jan _2 15:04:05", line[:15]); err == nil {
			t = t.AddDate(time.Now().Year(), 0, 0)
			row.Time = t.Unix()
			rest := strings.TrimSpace(line[16:])
			if i := strings.IndexByte(rest, ' '); i > 0 {
				rest = strings.TrimSpace(rest[i+1:])
			}
			row.Message = rest
			if i := strings.IndexByte(rest, ':'); i > 0 {
				tag := rest[:i]
				row.Message = strings.TrimSpace(rest[i+1:])
				if a := strings.IndexByte(tag, '['); a > 0 && strings.HasSuffix(tag, "]") {
					row.Unit = tag[:a]
					row.PID = tag[a+1 : len(tag)-1]
				} else {
					row.Unit = tag
				}
			}
		}
	}
	low := strings.ToLower(line)
	switch {
	case strings.Contains(low, "emerg"), strings.Contains(low, "panic"):
		row.Priority = "emerg"
	case strings.Contains(low, "alert"):
		row.Priority = "alert"
	case strings.Contains(low, "crit"):
		row.Priority = "crit"
	case strings.Contains(low, " error"), strings.Contains(low, "err:"):
		row.Priority = "err"
	case strings.Contains(low, "warn"):
		row.Priority = "warning"
	case strings.Contains(low, "debug"):
		row.Priority = "debug"
	}
	return row
}

func journalPri(s string) string {
	switch s {
	case "0":
		return "emerg"
	case "1":
		return "alert"
	case "2":
		return "crit"
	case "3":
		return "err"
	case "4":
		return "warning"
	case "5":
		return "notice"
	case "7":
		return "debug"
	default:
		return "info"
	}
}

func normalizePri(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "0", "emerg", "emergency", "panic":
		return "emerg"
	case "1", "alert":
		return "alert"
	case "2", "crit", "critical":
		return "crit"
	case "3", "err", "error":
		return "err"
	case "4", "warning", "warn":
		return "warning"
	case "5", "notice":
		return "notice"
	case "7", "debug":
		return "debug"
	default:
		return "info"
	}
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			s := fmt.Sprint(v)
			if s != "" && s != "<nil>" {
				return s
			}
		}
	}
	return ""
}
