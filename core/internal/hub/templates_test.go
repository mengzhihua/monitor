package hub

import (
	"errors"
	"testing"

	"github.com/mengzhihua/monitor/core/internal/health"
)

func templateFixture(name string) Template {
	return Template{
		Name: name,
		Rules: []health.RuleSpec{
			{Name: "tpl.cpu", On: "system.cpu", Calc: "$user", Warn: "$this > 80", Every: "10s"},
		},
		Assign: TemplateAssign{Nodes: []string{"n1"}},
	}
}

func TestTemplatesUpsertCASAndDelete(t *testing.T) {
	dir := t.TempDir()
	tpl, err := OpenTemplates(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := tpl.Upsert(templateFixture("a-os"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == "" || a.Updated == 0 {
		t.Fatalf("want id+revision, got %+v", a)
	}
	// stale revision rejected
	if _, err := tpl.Upsert(templateFixture("a-os"), &[]int64{a.Updated + 9}[0]); !errors.Is(err, ErrTemplateConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	// correct revision accepted
	a.Name = "a-os-v2"
	upd, err := tpl.Upsert(Template{ID: a.ID, Name: a.Name, Assign: TemplateAssign{Nodes: []string{"n1"}}}, &a.Updated)
	if err != nil || upd.Updated <= a.Updated {
		t.Fatalf("update: %v %+v", err, upd)
	}
	// reopen persists
	again, err := OpenTemplates(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := again.Get(a.ID); !ok || got.Name != "a-os-v2" {
		t.Fatalf("reload: %+v %v", got, ok)
	}
	if err := tpl.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := tpl.Delete(a.ID); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("want notfound, got %v", err)
	}
}

func TestTemplatesUpsertRejectsBadRule(t *testing.T) {
	tpl, err := OpenTemplates(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bad := templateFixture("bad")
	bad.Rules[0].Warn = "$this >{$MISSING" // unknown macro
	if _, err := tpl.Upsert(bad, nil); !errors.Is(err, ErrTemplateInvalid) {
		t.Fatalf("want invalid, got %v", err)
	}
	if len(tpl.List()) != 0 {
		t.Fatal("invalid template must not persist")
	}
}

func TestTemplatesEffectiveMerge(t *testing.T) {
	tpl, err := OpenTemplates(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rule := func(name, warn string) health.RuleSpec {
		return health.RuleSpec{Name: name, On: "system.cpu", Calc: "$user", Warn: warn, Every: "10s"}
	}
	// "b" wins over "a" by Name sort
	a, _ := tpl.Upsert(Template{
		Name: "a-base", Macros: map[string]string{"A": "1", "SHARED": "a"},
		Rules: []health.RuleSpec{rule("r1", "$this > {$A}")}, Removed: []string{"base.x"},
		Disabled: []string{"apps"}, Tags: map[string]string{"env": "prod", "owner": "a"},
		Assign: TemplateAssign{Rooms: []string{"rm1"}},
	}, nil)
	b, err := tpl.Upsert(Template{
		Name: "b-over", Macros: map[string]string{"SHARED": "b"},
		Rules:   []health.RuleSpec{rule("r1", "$this > 99"), rule("r2", "$this > 1")},
		Removed: []string{"base.y"}, Disabled: []string{"swap"},
		Tags:   map[string]string{"owner": "b"},
		Assign: TemplateAssign{Rooms: []string{"rm1"}, Labels: map[string]string{"env": "prod", "dc": "us"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	ov := tpl.Effective("any", "rm1", nil)
	if ov.Rev != max64(a.Updated, b.Updated) || len(ov.Templates) != 2 {
		t.Fatalf("room match: %+v", ov)
	}
	if ov.Macros["SHARED"] != "b" {
		t.Fatalf("later template wins macro: %v", ov.Macros)
	}
	byName := map[string]health.RuleSpec{}
	for _, r := range ov.Rules {
		byName[r.Name] = r
	}
	if byName["r1"].Warn != "$this > 99" {
		t.Fatalf("later template wins rule: %+v", byName["r1"])
	}
	if len(byName) != 2 {
		t.Fatalf("rules: %v", byName)
	}
	for _, want := range []string{"base.x", "base.y"} {
		found := false
		for _, r := range ov.Removed {
			found = found || r == want
		}
		if !found {
			t.Fatalf("removed union missing %s: %v", want, ov.Removed)
		}
	}
	if ov.Tags["env"] != "prod" || ov.Tags["owner"] != "b" {
		t.Fatalf("tags: %v", ov.Tags)
	}
	if ov.RuleSource["r2"] != "b-over" {
		t.Fatalf("rule_source: %v", ov.RuleSource)
	}

	// label AND-match: missing one label → no match
	ov = tpl.Effective("n2", "", map[string]string{"env": "prod"})
	if len(ov.Templates) != 0 || ov.Rev != 0 {
		t.Fatalf("label AND-match should fail: %+v", ov)
	}
	ov = tpl.Effective("n2", "", map[string]string{"env": "prod", "dc": "us", "extra": "ok"})
	if len(ov.Templates) != 1 || ov.Templates[0] != "b-over" {
		t.Fatalf("label match: %+v", ov)
	}
	// explicit node match with empty labels
	ov = tpl.Effective("n1", "", nil)
	if len(ov.Templates) != 0 {
		t.Fatalf("n1 not in assign: %+v", ov)
	}
	// empty assign never matches
	empty, _ := tpl.Upsert(Template{Name: "c-empty", Assign: TemplateAssign{}}, nil)
	_ = empty
	ov = tpl.Effective("ghost", "rm9", map[string]string{"env": "prod"})
	for _, tn := range ov.Templates {
		if tn == "c-empty" {
			t.Fatal("empty assign must never match")
		}
	}
}

func TestTemplatesInventory(t *testing.T) {
	tpl, err := OpenTemplates(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inv, err := tpl.SetInventory("n1", map[string]string{"location": "dc1"}, nil)
	if err != nil || inv.Updated == 0 {
		t.Fatalf("%v %+v", err, inv)
	}
	if _, err := tpl.SetInventory("n1", map[string]string{}, &[]int64{999}[0]); !errors.Is(err, ErrTemplateConflict) {
		t.Fatalf("want conflict, %v", err)
	}
	got, ok := tpl.Inventory("n1")
	if !ok || got.Fields["location"] != "dc1" {
		t.Fatalf("%v %+v", ok, got)
	}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
