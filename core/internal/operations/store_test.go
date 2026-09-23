package operations

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

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
