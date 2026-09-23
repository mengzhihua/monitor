package operations

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestWorkflowMigratesLegacyAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acknowledgements.json")
	legacy := []byte(`{"version":1,"records":{"episode":{"id":"episode","acknowledged":true,"revision":1,"history":[{"at":1,"actor":"first","action":"acknowledge","note":"legacy note"}]}}}`)
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := s.Get("episode")
	if !r.Acknowledged || r.Status != StatusOpen || r.Assignee != "" || r.History[0].Note != "legacy note" {
		t.Fatal(r)
	}
	// Opening a legacy file alone does not rewrite it.
	if b, _ := os.ReadFile(path); string(b) != string(legacy) {
		t.Fatal("read unexpectedly rewrote legacy state")
	}
	for _, change := range []Change{{Action: "assign", Assignee: "on-call"}, {Action: "progress", Status: StatusInvestigating}, {Action: "assign", Assignee: "backup"}, {Action: "progress", Status: StatusWatching}} {
		r, err = s.Apply("episode", "operator", change, r.Revision, Target{Name: "memory"})
		if err != nil {
			t.Fatal(err)
		}
		if !r.Acknowledged {
			t.Fatal("workflow cleared acknowledgement")
		}
	}
	if r.History[3].PreviousAssignee != "on-call" || r.History[3].Assignee != "backup" || r.History[4].PreviousStatus != StatusInvestigating {
		t.Fatal(r.History)
	}
	var disk state
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &disk); err != nil || disk.Version != 2 {
		t.Fatalf("schema not upgraded: %s %v", b, err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r = s.Get("episode")
	if r.Assignee != "backup" || r.Status != StatusWatching || len(r.History) != 5 {
		t.Fatal(r)
	}
	if _, err := s.Apply("episode", "other", Change{Action: "assign", Assignee: "racing-owner"}, r.Revision-1, Target{}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	r, err = s.Apply("episode", "operator", Change{Action: "unassign"}, r.Revision, r.Problem)
	if err != nil || r.Assignee != "" || r.Status != StatusWatching || !r.Acknowledged || r.History[5].PreviousAssignee != "backup" {
		t.Fatalf("%+v %v", r, err)
	}
	if r := s.Get("new-episode"); r.Status != StatusOpen || r.Assignee != "" || r.Acknowledged {
		t.Fatal(r)
	}
	// A new binary must reject corrupt workflow rather than claim a clean queue.
	disk.Records["episode"] = Record{ID: "episode", Revision: 1, Status: "resolved"}
	b, _ = json.Marshal(disk)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("invalid workflow accepted")
	}
}

func TestWorkflowValidationAndFailedWrite(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []Change{
		{Action: "progress", Status: "resolved"}, {Action: "assign"}, {Action: "assign", Assignee: "unknown\nowner"},
		{Action: "comment", Note: "  "}, {Action: "assign", Assignee: "owner", Status: StatusWatching},
		{Action: "unassign", Assignee: "owner"}, {Action: "acknowledge", Status: StatusOpen},
	} {
		if _, err := s.Apply("episode", "operator", c, 0, Target{}); !errors.Is(err, ErrInvalidChange) {
			t.Fatalf("%+v: %v", c, err)
		}
	}
	if err := os.Mkdir(s.path, 0700); err != nil {
		t.Fatal(err)
	}
	for _, c := range []Change{{Action: "assign", Assignee: "operator"}, {Action: "progress", Status: StatusInvestigating}} {
		if _, err := s.Apply("episode", "operator", c, 0, Target{}); err == nil {
			t.Fatal("expected disk failure")
		}
		if r := s.Get("episode"); r.Assignee != "" || r.Status != StatusOpen || r.Revision != 0 {
			t.Fatal("failed write changed visible state", r)
		}
	}
}

func TestDurableAcknowledgementAndConflicts(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Update("episode", "operator", "acknowledge", "investigating", 0, Target{Hostname: "test", Name: "alarm"})
	if err != nil || !r.Acknowledged || r.Revision != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	r.History[0].Note = "mutated copy"
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Get("episode"); !got.Acknowledged || got.History[0].Note != "investigating" {
		t.Fatal(got)
	}
	if _, err := s.Update("episode", "another", "unacknowledge", "", 0, Target{Hostname: "test", Name: "alarm"}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if got := s.Get("new-episode"); got.Acknowledged {
		t.Fatal("new episode inherited acknowledgement")
	}
	for i := uint64(1); i < 30; i++ {
		if _, err := s.Update("episode", "operator", "comment", "follow-up", i, Target{Hostname: "test", Name: "alarm"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.Get("episode").History) != 20 {
		t.Fatal("unbounded action history")
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Update("episode", "operator", "unacknowledge", "", 30, Target{Hostname: "test", Name: "alarm"})
			if err == nil {
				mu.Lock()
				success++
				mu.Unlock()
			} else if !errors.Is(err, ErrConflict) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if success != 1 || s.Get("episode").Acknowledged {
		t.Fatalf("success=%d", success)
	}
}

func TestFailedWriteDoesNotAcknowledgeAndCorruptStateFails(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Force rename to fail without relying on OS permissions or effective uid.
	if err := os.Mkdir(s.path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Update("episode", "operator", "acknowledge", "", 0, Target{Hostname: "test", Name: "alarm"}); err == nil {
		t.Fatal("expected disk error")
	}
	if s.Get("episode").Acknowledged || s.Get("episode").Revision != 0 {
		t.Fatal("failed write changed state")
	}
	if err := os.Remove(s.path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "acknowledgements.json"), []byte(`{"version":1,"records":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("corrupt state was ignored")
	}
}
