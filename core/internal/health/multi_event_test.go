package health

import (
	"testing"
	"time"
)

func TestMultipleProblemEvents(t *testing.T) {
	const rules = `
alarms:
  - name: multi
    on: system.ram
    calc: '$used'
    every: 1s
    warn: '$this > 80'
    events: multiple
  - name: once
    on: system.ram
    calc: '$used'
    every: 1s
    warn: '$this > 80'
    events: single
`
	e, reg := newTestEngine(t, rules)
	now := time.Unix(1_700_000_000, 0)
	_ = reg.Collect("system.ram", now, map[string]float64{"used": 90, "free": 10})
	e.Tick(now.Add(time.Second))
	e.Tick(now.Add(3 * time.Second))
	var multi, once int
	var sawMultiple bool
	for _, entry := range e.Log(0) {
		switch entry.Name {
		case "multi":
			multi++
			if entry.Multiple {
				sawMultiple = true
			}
		case "once":
			once++
			if entry.Multiple {
				t.Fatal("single mode recorded a multiple event")
			}
		}
	}
	if multi != 2 || !sawMultiple {
		t.Fatalf("multiple events = %d saw=%v log=%+v", multi, sawMultiple, e.Log(0))
	}
	if once != 1 {
		t.Fatalf("single mode events = %d", once)
	}
	if _, err := Compile(RuleSpec{Name: "bad", On: "system.ram", Calc: "$this", Events: "weekly"}, "test"); err == nil {
		t.Fatal("accepted an unknown event mode")
	}
}

func TestDefaultEventModeStaysSingle(t *testing.T) {
	rule, err := Compile(RuleSpec{Name: "hot", On: "system.ram", Calc: "$this", Warn: "$this > 1"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if rule.Multiple {
		t.Fatal("empty events enabled multiple mode")
	}
}
