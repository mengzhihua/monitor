package ingest

import (
	"encoding/json"
	"testing"
)

func TestParseOTLPJSON(t *testing.T) {
	body := `{
	  "resourceMetrics": [{
	    "resource": {"attributes": [{"key":"service.name","value":{"stringValue":"api"}}]},
	    "scopeMetrics": [{
	      "metrics": [{
	        "name": "http.server.duration",
	        "gauge": {"dataPoints": [{"asDouble": 12.5, "attributes": [{"key":"http.method","value":{"stringValue":"GET"}}]}]},
	        "sum": null
	      }, {
	        "name": "requests_total",
	        "sum": {"isMonotonic": true, "dataPoints": [{"asInt": "9"}]}
	      }]
	    }]
	  }]
	}`
	samples, err := ParseOTLP([]byte(body), "application/json")
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 {
		t.Fatalf("len=%d %#v", len(samples), samples)
	}
	if samples[0].Name != "http_server_duration" || samples[0].Value != 12.5 || samples[0].Kind != KindGauge {
		t.Fatalf("gauge = %+v", samples[0])
	}
	foundSvc, foundMethod := false, false
	for _, l := range samples[0].Labels {
		if l.Key == "service.name" && l.Value == "api" {
			foundSvc = true
		}
		if l.Key == "http.method" && l.Value == "GET" {
			foundMethod = true
		}
	}
	if !foundSvc || !foundMethod {
		t.Fatalf("labels = %+v", samples[0].Labels)
	}
	if samples[1].Name != "requests_total" || samples[1].Value != 9 || samples[1].Kind != KindCounter {
		t.Fatalf("counter = %+v", samples[1])
	}
}

func TestParseOTLPLooksJSONWithoutType(t *testing.T) {
	b, _ := json.Marshal(otlpExport{ResourceMetrics: []otlpResourceMetrics{{
		ScopeMetrics: []otlpScopeMetric{{Metrics: []otlpMetric{{Name: "x", Gauge: &otlpNumber{DataPoints: []otlpPoint{{AsDouble: ptr(3.14)}}}}}}}},
	}})
	s, err := ParseOTLP(b, "")
	if err != nil || len(s) != 1 || s[0].Value != 3.14 {
		t.Fatalf("%v %#v", err, s)
	}
}

func ptr(f float64) *float64 { return &f }
