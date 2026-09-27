package operations

import (
	"fmt"
	"strings"
	"testing"
)

// Each query scans the full 5,000-episode retention limit with 20 retained
// 2,048-byte notes. Note strings are shared across episodes to keep fixture
// setup memory bounded; query/result allocations are included in measurement.
func BenchmarkHistorySearchLargeNotes(b *testing.B) {
	s, err := Open("")
	if err != nil {
		b.Fatal(err)
	}
	notes := make([]string, 20)
	for i := range notes {
		prefix := fmt.Sprintf("Investigation %02d: Memory Pressure. ", i)
		if i == 0 {
			prefix += "First-Note-Needle. "
		} else if i == len(notes)-1 {
			prefix += "Last-Note-Needle. "
		}
		notes[i] = (prefix + strings.Repeat("Inspect CPU Memory Storage. ", 100))[:2048]
	}
	for i := range 5000 {
		id := fmt.Sprintf("episode-%04d", i)
		actions := make([]Action, len(notes))
		for j, note := range notes {
			actions[j] = Action{At: int64(i + j + 1), Actor: "operator", Action: "comment", Note: note}
		}
		s.state.Records[id] = Record{ID: id, Revision: 20, Status: StatusOpen, Problem: Target{Node: "node", Hostname: "server-node", Name: "ram", Severity: "WARNING"}, History: actions}
	}
	for _, tc := range []struct {
		name   string
		filter HistoryFilter
		total  int
	}{
		{"metadata", HistoryFilter{Search: "server-node"}, 5000},
		{"first_note", HistoryFilter{Search: "first-note-needle"}, 5000},
		{"last_note", HistoryFilter{Search: "last-note-needle"}, 5000},
		{"missing", HistoryFilter{Search: "no-such-needle"}, 0},
		{"actor_missing", HistoryFilter{Search: "memory pressure", Actor: "another-operator"}, 0},
		{"field_boundary", HistoryFilter{Search: "node\nserver-node"}, 5000},
	} {
		b.Run(tc.name, func(b *testing.B) {
			q := HistoryQuery{HistoryFilter: tc.filter, Limit: 25}
			if got, err := s.QueryHistory(q); err != nil || got.Total != tc.total {
				b.Fatalf("total=%d want=%d err=%v", got.Total, tc.total, err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if got, err := s.QueryHistory(q); err != nil || got.Total != tc.total {
					b.Fatalf("total=%d want=%d err=%v", got.Total, tc.total, err)
				}
			}
		})
	}
}
