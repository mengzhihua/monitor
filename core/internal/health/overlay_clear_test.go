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
