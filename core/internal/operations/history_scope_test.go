package operations

import (
	"errors"
	"testing"
)

func TestHistoryNodeScopeFiltersBeforePaginationAndBindsSnapshots(t *testing.T) {
	s := historyFixture(t, 250)
	for id, r := range s.state.Records {
		if r.History[0].At > 10 {
			r.Problem.Node = "hidden"
			s.state.Records[id] = r
		}
	}
	scope := []string{"node", "local"}
	q := HistoryQuery{Limit: 7, ScopeNodes: scope}
	seen := map[string]bool{}
	var first HistoryPage
	for {
		page, err := s.QueryHistory(q)
		if err != nil || page.Total != 30 || page.Stored != 30 || len(page.Records) > 7 {
			t.Fatalf("scoped page=%+v err=%v", page, err)
		}
		if q.Cursor == "" {
			first = page
		}
		for _, r := range page.Records {
			if r.Problem.Node != "node" || seen[r.ID] {
				t.Fatal("hidden or repeated record", r.ID)
			}
			seen[r.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		q.Cursor = page.NextCursor
	}
	if len(seen) != 30 {
		t.Fatal("incomplete scoped history", len(seen))
	}
	all, err := s.QueryHistory(HistoryQuery{All: true, Snapshot: first.Snapshot, ScopeNodes: []string{"local", "node", "node"}})
	if err != nil || len(all.Records) != 30 || all.NextCursor != "" {
		t.Fatal("equivalent scope changed snapshot", all, err)
	}
	for _, nodes := range [][]string{nil, {}, {"hidden"}, {"node", "local", "new-node"}} {
		for _, query := range []HistoryQuery{{Cursor: first.NextCursor, ScopeNodes: nodes}, {All: true, Snapshot: first.Snapshot, ScopeNodes: nodes}} {
			if _, err := s.QueryHistory(query); !errors.Is(err, ErrHistoryChanged) {
				t.Fatal("changed authorization reused snapshot", nodes, err)
			}
		}
	}
	empty, err := s.QueryHistory(HistoryQuery{ScopeNodes: []string{}})
	if err != nil || empty.Total != 0 || empty.Stored != 0 || len(empty.Records) != 0 || empty.NextCursor != "" {
		t.Fatal("empty scope exposed history", empty, err)
	}
	hidden, err := s.QueryHistory(HistoryQuery{HistoryFilter: HistoryFilter{Search: "handover", Node: "hidden"}, ScopeNodes: scope})
	if err != nil || hidden.Total != 0 || hidden.Stored != 30 {
		t.Fatal("search bypassed scope", hidden, err)
	}
	if len(s.RecentForNodes(scope)) != 30 || len(s.RecentForNodes([]string{})) != 0 || len(s.Recent()) != 100 {
		t.Fatal("overview scope did not precede the result limit")
	}
	recent := s.RecentForNodes(scope)
	recent[0].History[0].Note = "modified"
	if s.Get(recent[0].ID).History[0].Note == "modified" {
		t.Fatal("overview exposed mutable history")
	}
	if scope[0] != "node" || scope[1] != "local" {
		t.Fatal("query mutated caller's scope")
	}
}
