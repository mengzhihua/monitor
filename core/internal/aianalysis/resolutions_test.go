package aianalysis

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fixturePlan() Plan {
	return Plan{Summary: "Check the available evidence", Hypotheses: []Hypothesis{{"Unknown workload", "Only a short window is available", "low"}}, Steps: []PlanStep{
		{ID: "s1", Title: "Check", Kind: "diagnostic", Instruction: "Inspect the observed values", Expected: "Confirm scope", Risk: "low", Chart: "test.analysis", Dimension: "value"},
		{ID: "s2", Title: "Review", Kind: "verification", Instruction: "Wait for new samples and review", Expected: "New evidence", Risk: "low"},
	}}
}
func fixtureResolution() Resolution {
	return Resolution{Node: "", Hostname: "test", Model: "fixture-model", Plan: fixturePlan(), Baseline: json.RawMessage(`{"node":"","host_id":"test","after":1,"before":301,"charts":[{"id":"test.analysis"}]}`)}
}
func TestPlanParserRejectsIncompleteAndUnsafeShapes(t *testing.T) {
	data, _ := json.Marshal(fixturePlan())
	for _, text := range []string{string(data), "```json\n" + string(data) + "\n```"} {
		if _, err := ParsePlan(text); err != nil {
			t.Fatal(err)
		}
	}
	for _, text := range []string{"null", "Here is a plan: " + string(data), string(data) + "{}", strings.Replace(string(data), `"summary":`, `"command":"execute", "summary":`, 1), strings.Repeat("x", 17<<10)} {
		if _, err := ParsePlan(text); !errors.Is(err, ErrProvider) {
			t.Fatal("accepted invalid provider format")
		}
	}
	for _, mutate := range []func(*Plan){
		func(p *Plan) { p.Steps[1].Kind = "diagnostic" },
		func(p *Plan) { p.Steps[0].Kind = "remediation"; p.Steps[0].Rollback = "" },
		func(p *Plan) { p.Steps[1].ID = p.Steps[0].ID },
		func(p *Plan) { p.Steps[0].Risk = "safe" },
	} {
		p := fixturePlan()
		mutate(&p)
		raw, _ := json.Marshal(p)
		if _, err := ParsePlan(string(raw)); err == nil {
			t.Fatal("accepted incomplete runbook")
		}
	}
}

func TestPlanRedactionPreservesSchemaForShortTokens(t *testing.T) {
	p := fixturePlan()
	p.Summary = "x"
	data, _ := json.Marshal(p)
	raw := strings.Replace(string(data), `"summary":"x"`, `"summary":"\u0078"`, 1)
	redacted := redactProviderContent(raw, "x")
	parsed, err := ParsePlan(redacted)
	if err != nil || parsed.Summary != "[redacted]" || parsed.Steps[0].Expected != p.Steps[0].Expected {
		t.Fatal("redaction corrupted schema or exposed escaped token")
	}
}
func TestResolutionPersistenceIsolationConflictsAndOwnership(t *testing.T) {
	dir, owner, other := t.TempDir(), ownerFor("one"), ownerFor("two")
	s, err := OpenResolutions(dir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Create(owner, fixtureResolution())
	if err != nil {
		t.Fatal(err)
	}
	if r.Revision != 1 || len(r.Progress) != 2 || r.Progress[0].Status != "todo" {
		t.Fatal("invalid initial progress")
	}
	if _, err := s.Get(other, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-account read")
	}
	if _, err := s.Update(other, r.ID, 1, "s1", "done", "", nil); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-account write")
	}
	r.Plan.Steps[0].Title = "client mutation"
	r, err = s.Update(owner, r.ID, 1, "s1", "done", "Actual evidence retained locally", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Plan.Steps[0].Title == "client mutation" || r.Progress[0].Status != "done" || r.Revision != 2 {
		t.Fatal("progress or isolation failed")
	}
	path := filepath.Join(dir, "ai-resolutions.json")
	before, _ := os.ReadFile(path)
	if _, err := s.Update(owner, r.ID, 1, "s1", "skipped", "stale", nil); !errors.Is(err, ErrConflict) {
		t.Fatal("stale update accepted")
	}
	if _, err := s.Update(owner, r.ID, 2, "fake", "done", "", nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown step accepted")
	}
	if err := s.Delete(owner, r.ID, 1); !errors.Is(err, ErrConflict) {
		t.Fatal("stale deletion accepted")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("rejected changes altered disk")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0600 {
			t.Fatal("private file permissions")
		}
	}
	s, err = OpenResolutions(dir)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := s.Get(owner, r.ID)
	if err != nil || restored.Progress[0].Note != r.Progress[0].Note || restored.Revision != 2 {
		t.Fatal("restart lost saved progress")
	}
	if err := s.Delete(owner, r.ID, 2); err != nil {
		t.Fatal(err)
	}
	s, err = OpenResolutions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(owner, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted resolution returned after restart")
	}
}
func TestResolutionCapacityAndCorruptStorage(t *testing.T) {
	s, _ := OpenResolutions("")
	owner := ownerFor("owner")
	for i := 0; i < ResolutionLimit; i++ {
		if _, err := s.Create(owner, fixtureResolution()); err != nil {
			t.Fatal(err)
		}
	}
	if !errors.Is(s.CanCreate(owner), ErrCapacity) {
		t.Fatal("preflight missed capacity")
	}
	if _, err := s.Create(owner, fixtureResolution()); !errors.Is(err, ErrCapacity) {
		t.Fatal("capacity exceeded")
	}
	for _, raw := range []string{`{}`, `null`, `{"version":1,"owners":null}`, `{"version":1,"owners":{}} {}`} {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "ai-resolutions.json"), []byte(raw), 0600)
		if _, err := OpenResolutions(dir); !errors.Is(err, ErrUnavailable) {
			t.Fatal("corrupt store accepted")
		}
	}
}
func TestResolutionPostRenameFailureStopsFurtherWrites(t *testing.T) {
	dir, owner := t.TempDir(), ownerFor("owner")
	s, _ := OpenResolutions(dir)
	r, err := s.Create(owner, fixtureResolution())
	if err != nil {
		t.Fatal(err)
	}
	s.syncDir = func(string) error { return errors.New("injected directory sync failure") }
	if _, err = s.Update(owner, r.ID, 1, "s1", "done", "", nil); !errors.Is(err, ErrUnavailable) {
		t.Fatal("ambiguous persistence reported success")
	}
	if _, err = s.Get(owner, r.ID); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unavailable store exposed stale state")
	}
	if err = s.Delete(owner, r.ID, 1); !errors.Is(err, ErrUnavailable) {
		t.Fatal("accepted stale deletion after committed rename")
	}
	s, err = OpenResolutions(dir)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.Get(owner, r.ID)
	if err != nil || r.Revision != 2 || r.Progress[0].Status != "done" {
		t.Fatal("restart did not recover committed state")
	}
}
