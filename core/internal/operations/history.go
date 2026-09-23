package operations

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"
)

var ErrHistoryChanged = errors.New("handling history changed; restart the query")
var ErrInvalidQuery = errors.New("invalid history query or cursor")

// HistoryFilter matches stored episodes, not only the latest overview entries.
// From/Until are inclusive bounds on the most recent retained action's time.
type HistoryFilter struct {
	Search       string `json:"q"`
	Node         string `json:"node"`
	Assignee     string `json:"assignee"`
	Actor        string `json:"actor"`
	Status       string `json:"status"`
	Severity     string `json:"severity"`
	Acknowledged string `json:"acknowledged"`
	From         int64  `json:"from"`
	Until        int64  `json:"until"`
}

type HistoryQuery struct {
	HistoryFilter
	Limit    int
	Cursor   string
	Snapshot string
	All      bool // Only used by the bounded export endpoint, never by list pagination.
}

type HistoryRecord struct {
	Record
	UpdatedAt        int64 `json:"updated_at"`
	HistoryTruncated bool  `json:"history_truncated"`
}

type HistoryPage struct {
	Records          []HistoryRecord `json:"records"`
	Total            int             `json:"total"`
	Stored           int             `json:"stored"`
	Capacity         int             `json:"capacity"`
	ActionsPerRecord int             `json:"actions_per_record"`
	Snapshot         string          `json:"snapshot"`
	NextCursor       string          `json:"next_cursor"`
	Filter           HistoryFilter   `json:"filter"`
}

type historyCursor struct {
	Version  int    `json:"v"`
	Snapshot string `json:"s"`
	Filter   string `json:"f"`
	Offset   int    `json:"o"`
}

func digest(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func updatedAt(r Record) int64 {
	if len(r.History) == 0 {
		return 0
	}
	return r.History[len(r.History)-1].At
}

// QueryHistory copies one consistent page. A write invalidates old cursors and
// exports so concurrent edits cannot silently skip or duplicate sorted entries.
func (s *Store) QueryHistory(q HistoryQuery) (HistoryPage, error) {
	q.Search = strings.ToLower(strings.TrimSpace(q.Search))
	q.Node, q.Assignee, q.Actor = strings.TrimSpace(q.Node), strings.TrimSpace(q.Assignee), strings.TrimSpace(q.Actor)
	if len(q.Search) > 256 || len(q.Node) > 256 || len(q.Assignee) > 128 || len(q.Actor) > 128 || !utf8.ValidString(q.Search+q.Node+q.Assignee+q.Actor) ||
		(q.Status != "" && !validStatus(q.Status)) || (q.Severity != "" && q.Severity != "WARNING" && q.Severity != "CRITICAL") ||
		(q.Acknowledged != "" && q.Acknowledged != "true" && q.Acknowledged != "false") ||
		q.From < 0 || q.Until < 0 || q.From > 253402300799 || q.Until > 253402300799 || (q.Until > 0 && q.From > q.Until) ||
		q.Limit < 0 || q.Limit > 100 || (q.All && q.Cursor != "") {
		return HistoryPage{}, ErrInvalidQuery
	}
	if q.Limit == 0 {
		q.Limit = 25
	}
	filterID := digest(q.HistoryFilter)
	offset := 0
	var cursor historyCursor
	if q.Cursor != "" {
		if len(q.Cursor) > 1024 {
			return HistoryPage{}, ErrInvalidQuery
		}
		b, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil || json.Unmarshal(b, &cursor) != nil || cursor.Version != 1 || cursor.Filter != filterID || len(cursor.Snapshot) != 64 || cursor.Offset < 0 {
			return HistoryPage{}, ErrInvalidQuery
		}
		offset = cursor.Offset
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.historyVersion == "" {
		// A process restart also invalidates cursors. Do not serialize all note
		// contents merely to detect edits; Apply invalidates this generation.
		s.historyVersion = digest(rand.Text())
	}
	if (q.Snapshot != "" && q.Snapshot != s.historyVersion) || (q.Cursor != "" && cursor.Snapshot != s.historyVersion) {
		return HistoryPage{}, ErrHistoryChanged
	}
	out := HistoryPage{Records: []HistoryRecord{}, Stored: len(s.state.Records), Capacity: 5000, ActionsPerRecord: 20, Snapshot: s.historyVersion, Filter: q.HistoryFilter}
	matches := make([]Record, 0)
	for _, r := range s.state.Records {
		at := updatedAt(r)
		if (q.Node != "" && r.Problem.Node != q.Node) || (q.Assignee != "" && r.Assignee != q.Assignee) ||
			(q.Status != "" && r.Status != q.Status) || (q.Severity != "" && r.Problem.Severity != q.Severity) ||
			(q.Acknowledged != "" && r.Acknowledged != (q.Acknowledged == "true")) || at < q.From || (q.Until > 0 && at > q.Until) {
			continue
		}
		actorMatches := q.Actor == ""
		texts := []string{r.ID, r.Problem.Node, r.Problem.Hostname, r.Problem.Chart, r.Problem.Name, r.Assignee}
		for _, h := range r.History {
			actorMatches = actorMatches || h.Actor == q.Actor
			if q.Search != "" {
				texts = append(texts, h.Actor, h.Note, h.Assignee, h.PreviousAssignee)
			}
		}
		if !actorMatches || (q.Search != "" && !strings.Contains(strings.ToLower(strings.Join(texts, "\n")), q.Search)) {
			continue
		}
		matches = append(matches, r)
	}
	sort.Slice(matches, func(i, j int) bool {
		a, b := updatedAt(matches[i]), updatedAt(matches[j])
		if a != b {
			return a > b
		}
		return matches[i].ID < matches[j].ID
	})
	out.Total = len(matches)
	if offset > out.Total {
		return HistoryPage{}, ErrInvalidQuery
	}
	end := min(offset+q.Limit, out.Total)
	if q.All {
		end = out.Total
	}
	for _, r := range matches[offset:end] {
		r.History = append([]Action{}, r.History...)
		out.Records = append(out.Records, HistoryRecord{Record: r, UpdatedAt: updatedAt(r), HistoryTruncated: r.Revision > uint64(len(r.History))})
	}
	if end < out.Total {
		b, _ := json.Marshal(historyCursor{Version: 1, Snapshot: out.Snapshot, Filter: filterID, Offset: end})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return out, nil
}
