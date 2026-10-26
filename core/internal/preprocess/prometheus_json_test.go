package preprocess

import (
	"encoding/json"
	"math"
	"testing"
)

const promJSONText = `# TYPE http_requests_total counter
http_requests_total{code="200",path="/api}v1"} 10
http_requests_total{code="500",path="/"} 2
up 1
bad NaN
`

func TestPrometheusToJSON(t *testing.T) {
	v, _, err := Apply([]Step{
		{Type: "prometheus_to_json", Pattern: `http_requests_total{code="200"}`},
		{Type: "jsonpath", Path: "$[0].value"},
	}, promJSONText, math.NaN())
	if err != nil || v != 10 {
		t.Fatalf("selected %v %v", v, err)
	}
	raw, err := applyPromJSON(Step{Type: "prometheus_to_json", Pattern: `http_requests_total{code=~"5.."}`}, promJSONText)
	if err != nil {
		t.Fatal(err)
	}
	var rows []promJSONSample
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Value != "2" || rows[0].Type != "counter" || rows[0].Labels["path"] != "/" {
		t.Fatalf("%+v", rows)
	}
	raw, err = applyPromJSON(Step{Type: "prometheus_to_json"}, promJSONText)
	if err != nil {
		t.Fatal(err)
	}
	rows = nil
	if err := json.Unmarshal([]byte(raw), &rows); err != nil || len(rows) != 3 || rows[2].Name != "up" || rows[2].Type != "untyped" {
		t.Fatalf("all %+v %v", rows, err)
	}
	if _, err = applyPromJSON(Step{Type: "prometheus_to_json", Pattern: "missing"}, promJSONText); err == nil {
		t.Fatal("accepted a missing metric")
	}
	if _, err = applyPromJSON(Step{Type: "prometheus_to_json", Pattern: "up extra"}, promJSONText); err == nil {
		t.Fatal("accepted a trailing pattern")
	}
}
