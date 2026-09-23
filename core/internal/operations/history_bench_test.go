package operations

import (
	"fmt"
	"testing"
)

// Full-capacity fixture with 20 retained actions per episode, populated outside
// the measured interval. This measures reading a stable operational history.
func BenchmarkHistoryReads(b *testing.B) {
	s, err := Open("")
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 5000; i++ {
		id := fmt.Sprintf("episode-%04d", i)
		actions := make([]Action, 20)
		for j := range actions {
			actions[j] = Action{At: int64(i/3 + j), Actor: "operator", Action: "comment", Note: "investigating host memory pressure"}
		}
		s.state.Records[id] = Record{ID: id, Revision: 20, Status: StatusOpen, Problem: Target{Node: "node", Hostname: "server", Name: "ram", Severity: "WARNING"}, History: actions}
	}
	cases := []struct {
		name  string
		query HistoryQuery
	}{
		{"page", HistoryQuery{Limit: 25}},
		{"filtered_page", HistoryQuery{HistoryFilter: HistoryFilter{Node: "node", Actor: "operator"}, Limit: 25}},
		{"search", HistoryQuery{HistoryFilter: HistoryFilter{Search: "memory pressure"}, Limit: 25}},
		{"export", HistoryQuery{All: true}},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			if _, err := s.QueryHistory(tc.query); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := s.QueryHistory(tc.query); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	b.Run("overview_recent", func(b *testing.B) {
		s.Recent()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			s.Recent()
		}
	})
}
