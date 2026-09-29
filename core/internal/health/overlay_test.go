package health

import (
	"testing"
)

// SetOverlay layering: template rules merge over base, runtime overrides win
// over both, template Removed hides base rules, and a runtime Restore does not
// resurrect a template-removed rule.
func TestSetOverlayLayering(t *testing.T) {
	dir := t.TempDir()
	e := ruleConfigEngine(t, dir,
		ruleConfigFixture("base.keep"),
		ruleConfigFixture("base.hide"),
		ruleConfigFixture("tpl.shared"))

	overlay := OverlayLayer{
		Rev:       7,
		Templates: []string{"tpl"},
		Macros:    map[string]string{"T": "90"},
		Rules: []RuleSpec{
			{Name: "tpl.shared", On: "system.ram", Calc: "$used", Warn: "$this > 1", Every: "1s"},
			{Name: "tpl.only", On: "system.ram", Calc: "$used", Warn: "$this > {$T}", Every: "1s"},
		},
		Removed: []string{"base.hide"},
	}
	if err := e.SetOverlay(overlay); err != nil {
		t.Fatal(err)
	}
	snap := e.RulesConfig()
	names := map[string]*Rule{}
	for _, r := range snap.Rules {
		names[r.Spec.Name] = r
	}
	if _, ok := names["base.hide"]; ok {
		t.Fatal("template Removed must hide the base rule")
	}
	if names["tpl.shared"].Spec.Warn != "$this > 1" || names["tpl.only"] == nil {
		t.Fatalf("overlay rules not merged: %+v", names)
	}
	if len(snap.Templates) != 1 || snap.Templates[0] != "tpl" {
		t.Fatalf("templates provenance: %v", snap.Templates)
	}
	if len(snap.TemplateRules) != 2 {
		t.Fatalf("template_rules: %v", snap.TemplateRules)
	}

	// runtime override wins over the template rule
	if _, err := e.MutateRules(nil, RuleMutation{Upserts: []RuleSpec{
		{Name: "tpl.shared", On: "system.ram", Calc: "$used", Warn: "$this > 2", Every: "1s"},
	}}); err != nil {
		t.Fatal(err)
	}
	snap = e.RulesConfig()
	for _, r := range snap.Rules {
		if r.Spec.Name == "tpl.shared" && r.Spec.Warn != "$this > 2" { // runtime override must win
			t.Fatalf("runtime override must win: %+v", r.Spec)
		}
	}

	// runtime restore does not resurrect base.hide — deleting it fails since it
	// is already absent from the effective set, and Restore has no tombstone.
	if _, err := e.MutateRules(nil, RuleMutation{Delete: []string{"base.hide"}}); err == nil {
		t.Fatal("deleting a template-removed rule should fail")
	}
	_, _ = e.MutateRules(nil, RuleMutation{Restore: []string{"base.hide"}})
	for _, r := range e.RulesConfig().Rules {
		if r.Spec.Name == "base.hide" {
			t.Fatal("restore resurrected a template-removed rule")
		}
	}
}

// A bad overlay is rejected whole and the previous overlay stays in effect.
func TestSetOverlayRejected(t *testing.T) {
	dir := t.TempDir()
	e := ruleConfigEngine(t, dir, ruleConfigFixture("base.keep"))
	good := OverlayLayer{Rev: 1, Rules: []RuleSpec{
		{Name: "tpl.ok", On: "system.ram", Calc: "$used", Warn: "$this > 1", Every: "1s"},
	}}
	if err := e.SetOverlay(good); err != nil {
		t.Fatal(err)
	}
	bad := OverlayLayer{Rev: 2, Rules: []RuleSpec{
		{Name: "tpl.bad", On: "system.ram", Calc: "$used", Warn: "$this > {$NOPE}", Every: "1s"},
	}}
	if err := e.SetOverlay(bad); err == nil {
		t.Fatal("invalid overlay must be rejected")
	}
	snap := e.RulesConfig()
	found := map[string]bool{}
	for _, r := range snap.Rules {
		found[r.Spec.Name] = true
	}
	if !found["tpl.ok"] || found["tpl.bad"] {
		t.Fatalf("rejected overlay leaked rules: %v", found)
	}
}

// The overlay survives restart via template-overlay.json.
func TestSetOverlayPersistReload(t *testing.T) {
	dir := t.TempDir()
	e := ruleConfigEngine(t, dir, ruleConfigFixture("base.keep"))
	if err := e.SetOverlay(OverlayLayer{Rev: 3, Templates: []string{"os"}, Rules: []RuleSpec{
		{Name: "tpl.reload", On: "system.ram", Calc: "$used", Warn: "$this > 1", Every: "1s"},
	}}); err != nil {
		t.Fatal(err)
	}
	e.Close()

	e2 := ruleConfigEngine(t, dir, ruleConfigFixture("base.keep"))
	defer e2.Close()
	snap := e2.RulesConfig()
	found := false
	for _, r := range snap.Rules {
		found = found || r.Spec.Name == "tpl.reload"
	}
	if !found || len(snap.Templates) != 1 {
		t.Fatalf("overlay not reloaded: %v templates=%v", found, snap.Templates)
	}
}
