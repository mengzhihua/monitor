package operations

import (
	"errors"
	"fmt"
	"os"
	"testing"
)

func historyFixture(t *testing.T, count int) *Store {
	t.Helper()
	s, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("episode-%03d", i)
		_, err := s.Update(id, "operator", "comment", fmt.Sprintf("handover %03d", i), 0, Target{Node: "node", Hostname: "server", Name: "ram", Severity: "WARNING"})
		if err != nil {
			t.Fatal(err)
		}
		// Deterministic timestamps include ties to exercise the stable ID order.
		r := s.state.Records[id]
		r.History[0].At = int64(i/3 + 1)
		s.state.Records[id] = r
	}
	return s
}

func TestHistoryBeyondOverviewPaginationAndFilters(t *testing.T) {
	s := historyFixture(t, 130)
	if len(s.Recent()) != 100 {
		t.Fatal("overview compatibility changed")
	}
	q := HistoryQuery{Limit: 17}
	seen := map[string]bool{}
	var first HistoryPage
	for {
		p, err := s.QueryHistory(q)
		if err != nil || p.Total != 130 || p.Stored != 130 || len(p.Records) > 17 {
			t.Fatalf("%+v %v", p, err)
		}
		if q.Cursor == "" {
			first = p
		}
		for _, r := range p.Records {
			if seen[r.ID] {
				t.Fatal("duplicate episode", r.ID)
			}
			seen[r.ID] = true
		}
		if p.NextCursor == "" {
			break
		}
		q.Cursor = p.NextCursor
	}
	if len(seen) != 130 || !seen["episode-000"] {
		t.Fatal("oldest history inaccessible")
	}
	q = HistoryQuery{HistoryFilter: HistoryFilter{Search: "HANDOVER 000", Actor: "operator", Node: "node", Severity: "WARNING", Status: StatusOpen, Acknowledged: "false", From: 1, Until: 1}}
	p, err := s.QueryHistory(q)
	if err != nil || p.Total != 1 || p.Records[0].ID != "episode-000" {
		t.Fatalf("%+v %v", p, err)
	}
	p.Records[0].History[0].Note = "mutated"
	if s.Get("episode-000").History[0].Note != "handover 000" {
		t.Fatal("query leaked mutable history")
	}
	all, err := s.QueryHistory(HistoryQuery{All: true, Snapshot: first.Snapshot})
	if err != nil || len(all.Records) != 130 || all.NextCursor != "" {
		t.Fatalf("incomplete export %d %v", len(all.Records), err)
	}
	if _, err := s.QueryHistory(HistoryQuery{HistoryFilter: HistoryFilter{Search: "different"}, Cursor: first.NextCursor}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatal(err)
	}
	if _, err := s.Update("episode-000", "operator", "comment", "changed", 1, Target{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueryHistory(HistoryQuery{Cursor: first.NextCursor}); !errors.Is(err, ErrHistoryChanged) {
		t.Fatal("stale pagination accepted", err)
	}
	if _, err := s.QueryHistory(HistoryQuery{All: true, Snapshot: first.Snapshot}); !errors.Is(err, ErrHistoryChanged) {
		t.Fatal("stale export accepted", err)
	}
}

func TestHistoryRetentionValidationAndFailedWriteSnapshot(t *testing.T) {
	s := historyFixture(t, 1)
	r := s.Get("episode-000")
	for i := 0; i < 25; i++ {
		var err error
		r, err = s.Update(r.ID, "second", "comment", "followup", r.Revision, r.Problem)
		if err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.QueryHistory(HistoryQuery{HistoryFilter: HistoryFilter{Actor: "second"}})
	if err != nil || p.Total != 1 || !p.Records[0].HistoryTruncated || len(p.Records[0].History) != 20 {
		t.Fatal(p, err)
	}
	p, _ = s.QueryHistory(HistoryQuery{HistoryFilter: HistoryFilter{Actor: "operator"}})
	if p.Total != 0 {
		t.Fatal("matched an action outside retained history")
	}
	for _, q := range []HistoryQuery{{Limit: 101}, {Cursor: "bogus"}, {HistoryFilter: HistoryFilter{From: 10, Until: 1}}, {HistoryFilter: HistoryFilter{Status: "resolved"}}, {HistoryFilter: HistoryFilter{Acknowledged: "yes"}}, {HistoryFilter: HistoryFilter{From: -1}}} {
		if _, err := s.QueryHistory(q); !errors.Is(err, ErrInvalidQuery) {
			t.Fatal(q, err)
		}
	}
	dir := t.TempDir()
	disk, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := disk.QueryHistory(HistoryQuery{})
	if err := os.Mkdir(disk.path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := disk.Update("fail", "operator", "comment", "note", 0, Target{}); err == nil {
		t.Fatal("disk failure expected")
	}
	if _, err := disk.QueryHistory(HistoryQuery{All: true, Snapshot: before.Snapshot}); err != nil {
		t.Fatal("failed write invalidated snapshot", err)
	}
	if err := os.Remove(disk.path); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.QueryHistory(HistoryQuery{Snapshot: before.Snapshot}); !errors.Is(err, ErrHistoryChanged) {
		t.Fatal("restart accepted an old snapshot", err)
	}
}
