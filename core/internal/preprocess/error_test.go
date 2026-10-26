package preprocess

import (
	"math"
	"strings"
	"testing"
)

func TestPreprocessErrorChecks(t *testing.T) {
	jsonSteps := []Step{{Type: "error_json", Path: "$.error"}, {Type: "jsonpath", Path: "$.value"}}
	v, _, err := Apply(jsonSteps, `{"value":"4"}`, math.NaN())
	if err != nil || v != 4 {
		t.Fatalf("clean json %v %v", v, err)
	}
	v, _, err = Apply(jsonSteps, `{"error":"","value":"4"}`, math.NaN())
	if err != nil || v != 4 {
		t.Fatalf("empty error field %v %v", v, err)
	}
	if _, _, err = Apply(jsonSteps, `{"error":"denied","value":"4"}`, math.NaN()); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("json error: %v", err)
	}
	if _, _, err = Apply(jsonSteps, `{`, math.NaN()); err == nil {
		t.Fatal("invalid json should fail")
	}
	if _, _, err = Apply([]Step{{Type: "error_json"}}, `{"value":"4"}`, math.NaN()); err == nil {
		t.Fatal("missing json path")
	}

	xmlSteps := []Step{{Type: "error_xml", Path: "/row/error"}, {Type: "xpath", Path: "/row/value"}}
	v, _, err = Apply(xmlSteps, `<row><value>7</value></row>`, math.NaN())
	if err != nil || v != 7 {
		t.Fatalf("clean xml %v %v", v, err)
	}
	v, _, err = Apply(xmlSteps, `<row><error></error><value>7</value></row>`, math.NaN())
	if err != nil || v != 7 {
		t.Fatalf("empty xml error %v %v", v, err)
	}
	if _, _, err = Apply(xmlSteps, `<row><error>disk full</error><value>7</value></row>`, math.NaN()); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("xml error: %v", err)
	}

	reSteps := []Step{{Type: "error_regex", Pattern: `ERROR: (.+)`}, {Type: "regex", Pattern: `([0-9]+)`}}
	v, _, err = Apply(reSteps, "ready 9", math.NaN())
	if err != nil || v != 9 {
		t.Fatalf("clean text %v %v", v, err)
	}
	if _, _, err = Apply(reSteps, "ERROR: disk", math.NaN()); err == nil || !strings.Contains(err.Error(), "disk") || strings.Contains(err.Error(), "ERROR:") {
		t.Fatalf("regex error group: %v", err)
	}
	if _, _, err = Apply([]Step{{Type: "error_regex", Pattern: "("}}, "x", math.NaN()); err == nil {
		t.Fatal("bad regex")
	}
}
