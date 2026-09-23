package operations

import (
	"errors"
	"reflect"
	"sync"
	"testing"
)

func TestOrderedHistoryRefreshesAfterBatchAndDoesNotLeakResults(t *testing.T) {
	s := historyFixture(t, 130)
	before, err := s.QueryHistory(HistoryQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	recent := s.Recent()
	recent[0].History[0].Note = "caller mutation"
	recent[0].ID = "caller mutation"
	before.Records[0].History[0].Note = "another mutation"
	first, _ := s.QueryHistory(HistoryQuery{Limit: 2})
	if first.Records[0].ID != "episode-129" || first.Records[0].History[0].Note != "handover 129" {
		t.Fatal("read mutated shared history", first)
	}
	_, err = s.ApplyBatch([]BatchItem{{ID: "episode-000", Revision: 1, Target: Target{Name: "changed"}}, {ID: "episode-001", Revision: 1, Target: Target{Name: "changed"}}}, "operator", Change{Action: "comment", Note: "batch refresh"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.QueryHistory(HistoryQuery{Limit: 2, Cursor: before.NextCursor}); !errors.Is(err, ErrHistoryChanged) {
		t.Fatal("batch did not invalidate cursor", err)
	}
	after, err := s.QueryHistory(HistoryQuery{Limit: 2})
	if err != nil || after.Records[0].ID != "episode-000" || after.Records[1].ID != "episode-001" {
		t.Fatal("cached order survived a batch write", after, err)
	}
	if got := s.Recent(); !reflect.DeepEqual(got[0], after.Records[0].Record) || got[0].History[1].Note != "batch refresh" {
		t.Fatal("overview differs from refreshed history", got[0])
	}
}

func TestHistoryFilteredPagesEqualExportAndPreserveSearchBoundaries(t *testing.T) {
	s := historyFixture(t, 31)
	// Missing actions sort at zero; negative legacy actions stay outside the
	// default From=0 history query, but remain visible in Recent as before.
	s.state.Records["empty"] = Record{ID: "empty", Revision: 1, Status: StatusOpen, History: []Action{}}
	s.state.Records["negative"] = Record{ID: "negative", Revision: 1, Status: StatusOpen, History: []Action{{At: -1}}}
	for _, filter := range []HistoryFilter{{}, {Actor: "operator", Node: "node"}, {Search: "HANDOVER"}, {Search: "node\nserver"}, {Status: StatusOpen, From: 3, Until: 7}, {Search: "no match"}} {
		q := HistoryQuery{HistoryFilter: filter, Limit: 3}
		var records []HistoryRecord
		snapshot := ""
		for {
			page, err := s.QueryHistory(q)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot == "" {
				snapshot = page.Snapshot
			}
			for _, r := range page.Records {
				if r.ID == "negative" {
					t.Fatal("negative timestamp unexpectedly included")
				}
			}
			records = append(records, page.Records...)
			if page.NextCursor == "" {
				if len(records) != page.Total {
					t.Fatal("incorrect filtered total")
				}
				break
			}
			q.Cursor = page.NextCursor
		}
		all, err := s.QueryHistory(HistoryQuery{HistoryFilter: filter, All: true, Snapshot: snapshot})
		if err != nil || len(records) != len(all.Records) {
			t.Fatal("export count differs", err)
		}
		for i := range records {
			if !reflect.DeepEqual(records[i], all.Records[i]) {
				t.Fatal("page/export ordering mismatch")
			}
		}
	}
	if len(s.Recent()) != 33 {
		t.Fatal("Recent lost a legacy record")
	}
	s, _ = Open("")
	page, err := s.QueryHistory(HistoryQuery{})
	if err != nil || page.Records == nil || page.Total != 0 || s.Recent() == nil {
		t.Fatal("empty arrays must stay non-null", page, err)
	}
}

func TestHistoryConcurrentReadsAndWrites(t *testing.T) {
	s := historyFixture(t, 25)
	var wg sync.WaitGroup
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 60; i++ {
				p, err := s.QueryHistory(HistoryQuery{Limit: 25})
				if err != nil || len(p.Records) != 25 {
					t.Error("incomplete concurrent read", err)
					return
				}
				for j := 1; j < len(p.Records); j++ {
					a, b := p.Records[j-1], p.Records[j]
					if a.UpdatedAt < b.UpdatedAt || (a.UpdatedAt == b.UpdatedAt && a.ID > b.ID) {
						t.Error("inconsistent history order")
						return
					}
				}
				p.Records[0].History[0].Note = "local copy"
				recent := s.Recent()
				recent[0].History[0].Note = "local copy"
			}
		}()
	}
	for i := 0; i < 40; i++ {
		r := s.Get("episode-000")
		if _, err := s.Update(r.ID, "operator", "comment", "new action", r.Revision, r.Problem); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if s.Get("episode-000").History[0].Note == "local copy" {
		t.Fatal("concurrent reader changed state")
	}
}
