package operations

import (
	"math/rand"
	"strings"
	"testing"
)

// Keep the pre-optimization matcher as an independent oracle for the exact
// set/order of searchable fields, separator behavior and Unicode lowercasing.
func joinedHistoryMatch(r Record, q HistoryFilter) bool {
	at := updatedAt(r)
	if (q.Node != "" && r.Problem.Node != q.Node) || (q.Assignee != "" && r.Assignee != q.Assignee) ||
		(q.Status != "" && r.Status != q.Status) || (q.Severity != "" && r.Problem.Severity != q.Severity) ||
		(q.Acknowledged != "" && r.Acknowledged != (q.Acknowledged == "true")) || at < q.From || (q.Until > 0 && at > q.Until) {
		return false
	}
	actorMatches := q.Actor == ""
	texts := []string{r.ID, r.Problem.Node, r.Problem.Hostname, r.Problem.Chart, r.Problem.Name, r.Assignee}
	for _, h := range r.History {
		actorMatches = actorMatches || h.Actor == q.Actor
		texts = append(texts, h.Actor, h.Note, h.Assignee, h.PreviousAssignee)
	}
	return actorMatches && (q.Search == "" || strings.Contains(strings.ToLower(strings.Join(texts, "\n")), q.Search))
}

func TestHistorySearchPreservesFieldsAndUnicode(t *testing.T) {
	r := Record{ID: "episode", Problem: Target{Node: "NODE", Hostname: "Server", Chart: "disk.space", Name: "Memory PRESSURE", Severity: "WARNING"}, Assignee: "on-call", Status: StatusOpen, History: []Action{
		{At: 1, Actor: "ALICE", Note: "Handover\nCAFÉ K İ Σ ς ſ ß", PreviousAssignee: "bob"},
		{At: 2, Actor: "BOB", Note: "legacy\xffvalue", Assignee: "backup", PreviousAssignee: "on-call", Action: "not-searchable", Status: "also-not-searchable"},
	}}
	for _, query := range []string{
		"episode", "node", "server", "disk.space", "pressure", "on-call", "alice", "handover", "backup", "bob",
		"CAFÉ", "k", "i", "σ", "ς", "ſ", "ß", "legacy�value", "no match",
		"node\nserver", "on-call\nalice", "handover\ncafé", "ß\n\nbob\nbob", "value\nbackup\non-call",
		"nodeserver", "not-searchable", "also-not-searchable", "warning",
	} {
		for _, actor := range []string{"", "ALICE", "BOB", "alice", "absent"} {
			q := HistoryFilter{Search: strings.ToLower(query), Actor: actor}
			if got, want := matchesHistory(r, q), joinedHistoryMatch(r, q); got != want {
				t.Fatalf("search=%q actor=%q got=%t want=%t", query, actor, got, want)
			}
		}
	}
	// Lowercasing is deliberately narrower than Unicode case folding.
	for _, tc := range []struct {
		note, query string
		want        bool
	}{{"ς", "σ", false}, {"ſ", "s", false}, {"ß", "ss", false}, {"İ", "i", true}, {"K", "k", true}} {
		r := Record{History: []Action{{At: 1, Note: tc.note}}}
		if got := matchesHistory(r, HistoryFilter{Search: tc.query}); got != tc.want {
			t.Fatalf("note=%q query=%q got=%t want=%t", tc.note, tc.query, got, tc.want)
		}
	}
}

func TestHistorySearchMatchesJoinedOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(438))
	pieces := []string{"", "a", "B", "NODE", "server", "\n", "\r\n", " ", "CAFÉ", "K", "İ", "ς", "ſ", "ß", "中", "😀", "\xff"}
	text := func() string {
		var b strings.Builder
		for n := rng.Intn(6); n > 0; n-- {
			b.WriteString(pieces[rng.Intn(len(pieces))])
		}
		return b.String()
	}
	for trial := range 200 {
		r := Record{ID: text(), Problem: Target{Node: text(), Hostname: text(), Chart: text(), Name: text(), Severity: "WARNING"}, Assignee: text(), Status: StatusOpen}
		for n := rng.Intn(21); n > 0; n-- {
			r.History = append(r.History, Action{At: 1, Actor: text(), Note: text(), Assignee: text(), PreviousAssignee: text()})
		}
		for range 30 {
			q := HistoryFilter{Search: strings.ToLower(text())}
			if rng.Intn(3) == 0 && len(r.History) > 0 {
				q.Actor = r.History[rng.Intn(len(r.History))].Actor
			}
			if got, want := matchesHistory(r, q), joinedHistoryMatch(r, q); got != want {
				t.Fatalf("trial=%d search=%q actor=%q got=%t want=%t record=%+v", trial, q.Search, q.Actor, got, want, r)
			}
		}
	}
}
