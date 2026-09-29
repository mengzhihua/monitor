package health

import "testing"

// An empty overlay clears previously applied template rules; reload after
// restart still yields no template rules.
func TestSetOverlayEmptyClears(t *testing.T) {
	dir := t.TempDir()
	e := ruleConfigEngine(t, dir, ruleConfigFixture("base.keep"))
	if err := e.SetOverlay(OverlayLayer{Rev: 1, Templates: []string{"t"}, Rules: []RuleSpec{
		{Name: "tpl.x", On: "system.ram", Calc: "$used", Warn: "$this > 1", Every: "1s"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := e.SetOverlay(OverlayLayer{}); err != nil {
		t.Fatal(err)
	}
	snap := e.RulesConfig()
	if len(snap.Templates) != 0 || len(snap.TemplateRules) != 0 {
		t.Fatalf("cleared overlay still shows templates: %+v", snap)
	}
	for _, r := range snap.Rules {
		if r.Spec.Name == "tpl.x" {
			t.Fatal("empty overlay did not clear template rule")
		}
	}
	e.Close()

	e2 := ruleConfigEngine(t, dir, ruleConfigFixture("base.keep"))
	defer e2.Close()
	snap = e2.RulesConfig()
	if len(snap.Templates) != 0 || len(snap.TemplateRules) != 0 {
		t.Fatalf("reloaded empty overlay: %+v", snap)
	}
}

// Runtime upserts may reference template macros (global < overlay).
func TestRuntimeRuleUsesOverlayMacro(t *testing.T) {
	dir := t.TempDir()
	e := ruleConfigEngine(t, dir, ruleConfigFixture("base.keep"))
	spec := RuleSpec{Name: "rt.macro", On: "system.ram", Calc: "$used", Warn: "$this > {$T}", Every: "1s"}
	// without the overlay macro the upsert must fail
	if _, err := e.MutateRules(nil, RuleMutation{Upserts: []RuleSpec{spec}}); err == nil {
		t.Fatal("upsert referencing undefined macro must fail")
	}
	if err := e.SetOverlay(OverlayLayer{Rev: 1, Macros: map[string]string{"T": "50"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.MutateRules(nil, RuleMutation{Upserts: []RuleSpec{spec}}); err != nil {
		t.Fatalf("upsert with overlay macro: %v", err)
	}
}

// SetOverlay bumps the snapshot revision; a pre-overlay CAS token is stale.
func TestSetOverlayChangesRevision(t *testing.T) {
	dir := t.TempDir()
	e := ruleConfigEngine(t, dir, ruleConfigFixture("base.keep"))
	before := e.RulesConfig().Revision
	if err := e.SetOverlay(OverlayLayer{Rev: 9, Templates: []string{"t"}}); err != nil {
		t.Fatal(err)
	}
	after := e.RulesConfig().Revision
	if before == after {
		t.Fatalf("revision unchanged by SetOverlay: %q", after)
	}
	if _, err := e.MutateRules(&before, RuleMutation{Upserts: []RuleSpec{ruleConfigFixture("x")}}); err == nil {
		t.Fatal("pre-overlay revision must conflict")
	}
}

// A macro-only overlay change re-evaluates alarm state for every rule.
func TestSetOverlayMacroChangeResetsAlarms(t *testing.T) {
	dir := t.TempDir()
	e := ruleConfigEngine(t, dir, ruleConfigFixture("base.keep"))
	if err := e.SetOverlay(OverlayLayer{Rev: 1, Macros: map[string]string{"T": "80"}, Rules: []RuleSpec{
		{Name: "tpl.x", On: "system.ram", Calc: "$used", Warn: "$this > {$T}", Every: "1s"},
	}}); err != nil {
		t.Fatal(err)
	}
	// raise the alarm: used=90 > 80
	e.reg.Collect("system.ram", e.now(), map[string]float64{"used": 90, "free": 10})
	e.Tick(e.now())
	found := false
	for _, a := range e.Alarms() {
		if a.Name == "tpl.x" && (a.Status == StatusWarning || a.Status == StatusCritical) {
			found = true
		}
	}
	if !found {
		t.Fatal("tpl.x alarm not raised")
	}
	// macro-only change: same rule spec, macro 80 -> 95. With used=90 the alarm
	// must be re-evaluated (cleared), i.e. the compiled threshold refreshed.
	if err := e.SetOverlay(OverlayLayer{Rev: 2, Macros: map[string]string{"T": "95"}, Rules: []RuleSpec{
		{Name: "tpl.x", On: "system.ram", Calc: "$used", Warn: "$this > {$T}", Every: "1s"},
	}}); err != nil {
		t.Fatal(err)
	}
	e.Tick(e.now())
	for _, a := range e.Alarms() {
		if a.Name == "tpl.x" && a.Status != StatusClear {
			t.Fatalf("macro change not applied: alarm still %v", a.Status)
		}
	}
}
