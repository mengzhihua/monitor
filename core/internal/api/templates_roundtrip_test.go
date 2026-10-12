package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/hub"
)

func TestTemplateEditorFlowYAMLRoundTrip(t *testing.T) {
	srv, _ := newTemplatesServer(t)
	// The browser editor emits a block mapping with JSON-encoded YAML values.
	// Exercise the actual YAML parser, rule validation and persisted API reply.
	want := health.RuleSpec{
		Name: "editor.roundtrip", On: "system.ram", Calc: "$used",
		Warn: "$this > {$LIMIT}", Every: "10s",
		Info:   "quoted \"value\": null\nsecond line\\path",
		Labels: map[string]string{"env": "true", "location": "a: b\n\"dc\""},
		Macros: map[string]string{"LIMIT": "85"},
	}
	rawRule, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rawRule, &fields); err != nil {
		t.Fatal(err)
	}
	yamlText := "alarms:\n  - \"name\": \"editor.roundtrip\"\n"
	for key, value := range fields {
		if key == "name" {
			continue
		}
		encodedKey, _ := json.Marshal(key)
		yamlText += "    " + string(encodedKey) + ": " + string(value) + "\n"
	}
	yamlText += "    \"disabled\": false\n"
	body, err := json.Marshal(map[string]any{"name": "editor flow", "rules_yaml": yamlText})
	if err != nil {
		t.Fatal(err)
	}
	resp := tplReq(t, http.MethodPut, srv.URL+"/api/v1/hub/templates", "admin", string(body))
	defer resp.Body.Close()
	var saved hub.Template
	if err := json.NewDecoder(resp.Body).Decode(&saved); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || len(saved.Rules) != 1 || !reflect.DeepEqual(saved.Rules[0], want) {
		t.Fatalf("saved template changed rule semantics: status=%d rules=%+v", resp.StatusCode, saved.Rules)
	}
	resp2 := tplReq(t, http.MethodGet, srv.URL+"/api/v1/hub/templates?id="+saved.ID, "admin", "")
	defer resp2.Body.Close()
	var loaded hub.Template
	if err := json.NewDecoder(resp2.Body).Decode(&loaded); err != nil {
		t.Fatal(err)
	}
	if resp2.StatusCode != http.StatusOK || !reflect.DeepEqual(loaded.Rules, saved.Rules) {
		t.Fatalf("persisted rules changed: %+v", loaded.Rules)
	}
}
