package health

import (
	"testing"
	"time"
)

func TestTriggerURLAndOpData(t *testing.T) {
	rule, err := Compile(RuleSpec{
		Name: "disk", On: "disk.space", Lookup: "average -1m of used", Warn: "$this > 1", Units: "%",
		URL:    "https://runbook.example/{HOST.NAME}",
		OpData: "used {EVENT.VALUE}{EVENT.UNITS}",
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	withMacro, err := Compile(RuleSpec{
		Name: "disk", On: "disk.space", Lookup: "average -1m of used", Warn: "$this > 1",
		URL:    "https://{$SITE}/runbook",
		Macros: map[string]string{"SITE": "example"},
	}, "test")
	if err != nil || withMacro.URL != "https://example/runbook" {
		t.Fatalf("macro url %v %v", withMacro, err)
	}
	if _, err := Compile(RuleSpec{Name: "disk", On: "disk.space", Lookup: "average -1m of used", URL: "javascript:alert(1)"}, "test"); err == nil {
		t.Fatal("accepted a non-http url")
	}
	if _, err := Compile(RuleSpec{Name: "disk", On: "disk.space", Lookup: "average -1m of used", OpData: "{EVENT.NOPE}"}, "test"); err == nil {
		t.Fatal("accepted an unknown token")
	}
	engine := &Engine{opt: Options{Hostname: "web-1"}}
	entry := engine.newEntry(&Alarm{
		ID: 3, Name: "disk", Chart: "disk.space", Units: "%", Info: "full",
		Value: 95, Status: StatusCritical, URL: rule.URL, OpData: rule.OpData,
	}, StatusClear, 10, time.Unix(100, 0))
	if entry.URL != "https://runbook.example/web-1" {
		t.Fatalf("url %q", entry.URL)
	}
	if entry.OpData != "used 95%" {
		t.Fatalf("opdata %q", entry.OpData)
	}
	notice, err := RenderMessage("{EVENT.NAME} {EVENT.OPDATA} {EVENT.URL}", entry)
	if err != nil || notice != "disk used 95% https://runbook.example/web-1" {
		t.Fatalf("notice %q %v", notice, err)
	}
}
