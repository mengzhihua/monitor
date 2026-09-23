package operations

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

func TestBatchAtomicWorkflowRestartAndIsolation(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	items := []BatchItem{{ID: "one", Target: Target{Name: "ram", Node: "host-a"}}, {ID: "two", Target: Target{Name: "ram", Node: "host-b"}}}
	before, _ := s.QueryHistory(HistoryQuery{})
	changes := []Change{{Action: "acknowledge", Note: "same incident"}, {Action: "assign", Assignee: "on-call"}, {Action: "progress", Status: StatusWatching}, {Action: "comment", Note: "follow-up"}, {Action: "unassign"}, {Action: "unacknowledge"}}
	for i, change := range changes {
		for j := range items {
			items[j].Revision = uint64(i)
		}
		records, err := s.ApplyBatch(items, "operator", change)
		if err != nil || len(records) != 2 {
			t.Fatal(records, err)
		}
		for j, r := range records {
			if r.ID != items[j].ID || r.Revision != uint64(i+1) || r.History[i].Actor != "operator" || r.History[i].Action != change.Action {
				t.Fatal(r)
			}
		}
		if records[0].History[i].At != records[1].History[i].At {
			t.Fatal("batch timestamps differ")
		}
		records[0].History[i].Note = "mutated result"
		if s.Get("one").History[i].Note == "mutated result" {
			t.Fatal("result aliases stored history")
		}
	}
	if _, err := s.QueryHistory(HistoryQuery{Snapshot: before.Snapshot}); !errors.Is(err, ErrHistoryChanged) {
		t.Fatal(err)
	}
	restarted, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		r := restarted.Get(item.ID)
		if !reflect.DeepEqual(r, s.Get(item.ID)) || r.Acknowledged || r.Assignee != "" || r.Status != StatusWatching || r.History[4].PreviousAssignee != "on-call" {
			t.Fatal(r)
		}
	}
}

func TestBatchConflictValidationAndDiskFailureLeaveEverythingUnchanged(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply("two", "other", Change{Action: "comment", Note: "existing"}, 0, Target{}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.QueryHistory(HistoryQuery{All: true})
	disk, _ := os.ReadFile(s.path)
	assertUnchanged := func() {
		t.Helper()
		after, err := s.QueryHistory(HistoryQuery{All: true, Snapshot: before.Snapshot})
		if err != nil || !reflect.DeepEqual(before, after) || s.Get("one").Revision != 0 {
			t.Fatal("batch partially published", err)
		}
	}
	for _, items := range [][]BatchItem{nil, {{ID: "one"}, {ID: "one"}}, {{ID: ""}}} {
		if _, err := s.ApplyBatch(items, "operator", Change{Action: "acknowledge"}); !errors.Is(err, ErrInvalidChange) {
			t.Fatal(err)
		}
		assertUnchanged()
	}
	items := []BatchItem{{ID: "one", Revision: 0}, {ID: "two", Revision: 0}}
	if _, err := s.ApplyBatch(items, "operator", Change{Action: "acknowledge"}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	assertUnchanged()
	if b, _ := os.ReadFile(s.path); string(b) != string(disk) {
		t.Fatal("conflict altered file")
	}
	items[1].Revision = 1
	if err := os.Remove(s.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyBatch(items, "operator", Change{Action: "acknowledge"}); err == nil {
		t.Fatal("expected rename failure")
	}
	assertUnchanged()
}

func TestBatchOverlappingWritersNeverPartiallyCommit(t *testing.T) {
	s, _ := Open(t.TempDir())
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Go(func() {
			items := []BatchItem{{ID: fmt.Sprintf("unique-%d", i)}, {ID: "shared"}}
			if _, err := s.ApplyBatch(items, "operator", Change{Action: "acknowledge"}); err == nil {
				success.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if success.Load() != 1 || len(s.state.Records) != 2 {
		t.Fatal("partial concurrent commit", success.Load(), len(s.state.Records))
	}
}

func TestBatchLimitAndCapacityAreCheckedForEntireBatch(t *testing.T) {
	s, _ := Open("")
	items := []BatchItem{}
	for i := range BatchLimit + 1 {
		items = append(items, BatchItem{ID: fmt.Sprintf("id-%d", i)})
	}
	if _, err := s.ApplyBatch(items, "operator", Change{Action: "comment", Note: "batch"}); !errors.Is(err, ErrInvalidChange) {
		t.Fatal(err)
	}
	if _, err := s.ApplyBatch(items[:BatchLimit], "operator", Change{Action: "comment", Note: "batch"}); err != nil {
		t.Fatal(err)
	}
	for i := len(s.state.Records); i < 4999; i++ {
		id := fmt.Sprintf("id-%d", i)
		s.state.Records[id] = Record{ID: id, Revision: 1, Status: StatusOpen, History: []Action{}}
	}
	if _, err := s.ApplyBatch([]BatchItem{{ID: "new-a"}, {ID: "new-b"}}, "operator", Change{Action: "acknowledge"}); err == nil {
		t.Fatal("capacity overflow accepted")
	}
	if len(s.state.Records) != 4999 || s.Get("new-a").Revision != 0 {
		t.Fatal("capacity failure partially committed")
	}
}
