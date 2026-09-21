package stream

import (
	"encoding/json"
	"testing"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestChartDefRoundTrip(t *testing.T) {
	c := &registry.Chart{ID: "net.eth0", Context: "net.net", Family: "eth0", Title: "Bandwidth", Units: "kb/s",
		Type: registry.Area, Priority: 500, UpdateEvery: 1, Plugin: "proc", Labels: map[string]string{"iface": "eth0"},
		Dimensions: []*registry.Dimension{
			{ID: "received", Name: "in", Algorithm: registry.Incremental, Multiplier: 8, Divisor: 1000},
			{ID: "sent", Name: "out", Algorithm: registry.Incremental, Multiplier: -8, Divisor: 1000, Hidden: true},
		}}
	def := DefOf(c)
	b, err := json.Marshal(Frame{Type: TypeChart, Chart: def})
	if err != nil {
		t.Fatal(err)
	}
	var f Frame
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if f.Type != TypeChart || f.Chart == nil || f.Chart.Fingerprint() != def.Fingerprint() {
		t.Fatalf("frame round trip mismatch: %s", b)
	}
	got := f.Chart.ToChart()
	if got.ID != c.ID || got.Type != c.Type || got.Labels["iface"] != "eth0" || len(got.Dimensions) != 2 {
		t.Fatalf("chart = %+v", got)
	}
	// values on the wire are post-algorithm: hub must store them as-is
	for _, d := range got.Dimensions {
		if d.Algorithm != registry.Absolute || d.Multiplier != 0 || d.Divisor != 0 {
			t.Fatalf("dimension %s should be plain absolute, got %+v", d.ID, d)
		}
	}
	if !got.Dimensions[1].Hidden || got.Dimensions[1].Name != "out" {
		t.Fatalf("hidden/name lost: %+v", got.Dimensions[1])
	}

	def2 := DefOf(c)
	def2.Dimensions = append(def2.Dimensions, DimDef{ID: "dropped"})
	if def2.Fingerprint() == def.Fingerprint() {
		t.Fatal("fingerprint must change with dimensions")
	}
}

func TestDestURL(t *testing.T) {
	cases := map[string]string{
		"hub:19999":                         "ws://hub:19999/api/v1/stream",
		"http://hub:19999":                  "ws://hub:19999/api/v1/stream",
		"https://hub.example.com/":          "wss://hub.example.com/api/v1/stream",
		"wss://hub.example.com/custom":      "wss://hub.example.com/custom",
		"ws://10.0.0.1:19999/api/v1/stream": "ws://10.0.0.1:19999/api/v1/stream",
	}
	for in, want := range cases {
		got, err := destURL(in)
		if err != nil || got != want {
			t.Errorf("destURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := destURL("ftp://hub"); err == nil {
		t.Fatal("expected error for ftp scheme")
	}
}

func TestDataFrameOmitsEmpty(t *testing.T) {
	b, _ := json.Marshal(Frame{Type: TypeData, ChartID: "system.cpu", T: 1700000000, V: map[string]float64{"user": 1.5}})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"host", "chart_def", "alarm", "call_id", "replay", "last"} {
		if _, ok := m[k]; ok {
			t.Errorf("data frame should omit %q: %s", k, b)
		}
	}
	if m["type"] != TypeData || m["chart"] != "system.cpu" {
		t.Fatalf("frame = %s", b)
	}
}
