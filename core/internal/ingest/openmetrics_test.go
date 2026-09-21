package ingest

import (
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestParseOpenMetrics(t *testing.T) {
	s := `# HELP http_requests_total ...
# TYPE http_requests_total counter
http_requests_total{method="GET",code="200"} 10
http_requests_total{method="GET",code="400"} 2
# TYPE temp gauge
temp 21.5
ignored_nan nan
# EOF
`
	samples := ParseOpenMetrics(s)
	if len(samples) != 3 {
		t.Fatalf("samples = %d %+v", len(samples), samples)
	}
	if samples[0].Kind != KindCounter || samples[0].Value != 10 || len(samples[0].Labels) != 2 {
		t.Fatalf("%+v", samples[0])
	}
	if samples[2].Name != "temp" || samples[2].Kind != KindGauge {
		t.Fatalf("%+v", samples[2])
	}
}

func TestMapperApply(t *testing.T) {
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	m := NewMapper(Options{Prefix: "prom", Plugin: "prometheus"})
	now := time.Unix(1_700_000_000, 0)
	samples := ParseOpenMetrics("# TYPE x_total counter\nx_total{a=\"b\"} 5\nx_total{a=\"c\"} 7\n")
	if n := m.Apply(reg, now, samples); n != 2 {
		t.Fatalf("applied %d", n)
	}
	ch, ok := reg.Chart("prom.x_total")
	if !ok || len(ch.Dims()) != 2 {
		t.Fatalf("chart %+v", ch)
	}
	_ = m.Apply(reg, now.Add(time.Second), ParseOpenMetrics("x_total{a=\"b\"} 15\nx_total{a=\"c\"} 9\n"))
	_, v := ch.LastValues()
	if v["a_b"] != 10 { // incremental
		t.Fatalf("rate = %v", v)
	}
}

func TestMapperCaps(t *testing.T) {
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	m := NewMapper(Options{Prefix: "om", MaxCharts: 1, MaxDims: 1})
	now := time.Unix(1, 0)
	m.Apply(reg, now, []Sample{{Name: "a", Value: 1}, {Name: "b", Value: 2}})
	if len(reg.Charts()) != 1 {
		t.Fatalf("charts = %d", len(reg.Charts()))
	}
}
