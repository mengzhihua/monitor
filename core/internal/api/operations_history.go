package api

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mengzhihua/monitor/core/internal/operations"
)

func historyQuery(r *http.Request, export bool) (operations.HistoryQuery, error) {
	v := r.URL.Query()
	q := operations.HistoryQuery{HistoryFilter: operations.HistoryFilter{
		Search: v.Get("q"), Node: v.Get("node"), Assignee: v.Get("assignee"), Actor: v.Get("actor"), Status: v.Get("status"),
		Severity: v.Get("severity"), Acknowledged: v.Get("acknowledged"),
	}, Limit: 25, Cursor: v.Get("cursor"), Snapshot: v.Get("snapshot"), All: export}
	for _, field := range []struct {
		key    string
		target *int64
	}{{"from", &q.From}, {"until", &q.Until}} {
		if value := v.Get(field.key); value != "" {
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return q, operations.ErrInvalidQuery
			}
			*field.target = n
		}
	}
	if value := v.Get("limit"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 100 {
			return q, operations.ErrInvalidQuery
		}
		q.Limit = n
	}
	if (q.Snapshot != "" && len(q.Snapshot) != 64) || (export && len(q.Snapshot) != 64) {
		return q, operations.ErrInvalidQuery
	}
	return q, nil
}

func historyError(w http.ResponseWriter, err error) {
	if errors.Is(err, operations.ErrHistoryChanged) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	http.Error(w, operations.ErrInvalidQuery.Error(), http.StatusBadRequest)
}

func (s *Server) handleOperationsHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	q, err := historyQuery(r, false)
	if err != nil {
		historyError(w, err)
		return
	}
	page, err := s.operations.QueryHistory(q)
	if err != nil {
		historyError(w, err)
		return
	}
	writeJSON(w, page)
}

// CSV readers may evaluate text as formulas even when CSV quotes are correct.
// Prefix hazardous text with an apostrophe; preserve the original in JSON.
func csvText(value string) string {
	trimmed := strings.TrimLeftFunc(value, func(r rune) bool { return unicode.IsSpace(r) || r == '\ufeff' })
	if trimmed != "" && strings.ContainsAny(trimmed[:1], "=+-@") || strings.HasPrefix(value, "\t") || strings.HasPrefix(value, "\r") || strings.HasPrefix(value, "\n") {
		return "'" + value
	}
	return value
}

func (s *Server) handleOperationsHistoryExport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	format := r.URL.Query().Get("format")
	if format != "json" && format != "csv" {
		historyError(w, operations.ErrInvalidQuery)
		return
	}
	q, err := historyQuery(r, true)
	if err != nil {
		historyError(w, err)
		return
	}
	page, err := s.operations.QueryHistory(q)
	if err != nil {
		historyError(w, err)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="monitor-handling-history-%s.%s"`, time.Now().UTC().Format("20060102T150405Z"), format))
	if format == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err := json.NewEncoder(w).Encode(page); err != nil {
			s.log.Debug("history export disconnected", "err", err)
		}
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	if _, err := w.Write([]byte("\ufeff")); err != nil {
		return
	}
	c := csv.NewWriter(w)
	_ = c.Write([]string{"episode_id", "node", "hostname", "alarm", "chart", "severity", "episode_since", "last_handled_at", "acknowledged", "assignee", "progress", "revision", "history_truncated", "action_at", "actor", "action", "note", "previous_assignee", "new_assignee", "previous_progress", "new_progress"})
	for _, r := range page.Records {
		actions := r.History
		if len(actions) == 0 {
			actions = []operations.Action{{}}
		}
		for _, a := range actions {
			row := []string{r.ID, r.Problem.Node, r.Problem.Hostname, r.Problem.Name, r.Problem.Chart, r.Problem.Severity,
				strconv.FormatInt(r.Problem.Since, 10), strconv.FormatInt(r.UpdatedAt, 10), strconv.FormatBool(r.Acknowledged), r.Assignee, r.Status, strconv.FormatUint(r.Revision, 10), strconv.FormatBool(r.HistoryTruncated),
				strconv.FormatInt(a.At, 10), a.Actor, a.Action, a.Note, a.PreviousAssignee, a.Assignee, a.PreviousStatus, a.Status}
			for i := range row {
				row[i] = csvText(row[i])
			}
			if err := c.Write(row); err != nil {
				return
			}
		}
	}
	c.Flush()
	if err := c.Error(); err != nil {
		s.log.Debug("history CSV export disconnected", "err", err)
	}
}
