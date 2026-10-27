package preprocess

import (
	"math"
	"testing"
)

const promText = `# HELP http_requests_total requests
# TYPE http_requests_total counter
http_requests_total{code="200",path="/api}v1"} 10
http_requests_total{code="500",path="/"} 2
up 1
weird NaN
`

func TestPrometheusPattern(t *testing.T) {
	steps := []Step{{Type: "prometheus", Pattern: `http_requests_total{code="200"}`}}
	v, _, err := Apply(steps, promText, math.NaN())
	if err != nil || v != 10 {
		t.Fatalf("one series %v %v", v, err)
	}
	sum, _, err := Apply([]Step{{Type: "prometheus", Pattern: "http_requests_total", Output: "sum"}}, promText, math.NaN())
	if err != nil || sum != 12 {
		t.Fatalf("sum %v %v", sum, err)
	}
	avg, _, err := Apply([]Step{{Type: "prometheus", Pattern: "http_requests_total", Output: "avg"}}, promText, math.NaN())
	if err != nil || avg != 6 {
		t.Fatalf("avg %v %v", avg, err)
	}
	min, _, err := Apply([]Step{{Type: "prometheus", Pattern: "http_requests_total", Output: "min"}}, promText, math.NaN())
	if err != nil || min != 2 {
		t.Fatalf("min %v %v", min, err)
	}
	matched, _, err := Apply([]Step{{Type: "prometheus", Pattern: `http_requests_total{code=~"5.."}`}}, promText, math.NaN())
	if err != nil || matched != 2 {
		t.Fatalf("regex %v %v", matched, err)
	}
	other, _, err := Apply([]Step{{Type: "prometheus", Pattern: `http_requests_total{code!="200"}`}}, promText, math.NaN())
	if err != nil || other != 2 {
		t.Fatalf("not equal %v %v", other, err)
	}
	text, err := applyPrometheus(Step{Type: "prometheus", Pattern: "up", Output: "label", Path: "job"}, "up{job=\"api\"} 1\n")
	if err != nil || text != "api" {
		t.Fatalf("label %q %v", text, err)
	}
	count, _, err := Apply([]Step{{Type: "prometheus", Pattern: "http_requests_total", Output: "count"}}, promText, math.NaN())
	if err != nil || count != 2 {
		t.Fatalf("count %v %v", count, err)
	}
	if _, _, err = Apply([]Step{{Type: "prometheus", Pattern: "http_requests_total"}}, promText, math.NaN()); err == nil {
		t.Fatal("accepted multiple matches without a summary")
	}
	if _, _, err = Apply([]Step{{Type: "prometheus", Pattern: "missing"}}, promText, math.NaN()); err == nil {
		t.Fatal("accepted a missing metric")
	}
	if _, _, err = Apply([]Step{{Type: "prometheus", Pattern: "up{code=\"200\"} extra"}}, promText, math.NaN()); err == nil {
		t.Fatal("accepted a trailing selector")
	}
	if _, _, err = Apply([]Step{{Type: "prometheus", Pattern: "", Output: "sum"}}, promText, math.NaN()); err == nil {
		t.Fatal("accepted an empty pattern")
	}
	plain, _, err := Apply([]Step{{Type: "prometheus", Pattern: "up"}}, promText, math.NaN())
	if err != nil || plain != 1 {
		t.Fatalf("bare metric %v %v", plain, err)
	}
}
