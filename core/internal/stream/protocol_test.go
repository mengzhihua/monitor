package stream

import (
	"encoding/json"
	"strings"
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
	got, err := destURLPath("hub:19999", PathACLK)
	if err != nil || got != "ws://hub:19999/api/v1/aclk" {
		t.Fatalf("aclk dest = %q %v", got, err)
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

// roundTrip marshals f and unmarshals it back into a fresh Frame.
func roundTrip(t *testing.T, f Frame) Frame {
	t.Helper()
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Frame
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func eqStrings(t *testing.T, name string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

// A config frame (hub → agent) keeps the overlay and the full-replacement
// fields through a JSON round-trip.
func TestConfigFrameRoundTrip(t *testing.T) {
	yaml := "stream:\n  destinations:\n    - hub:1443\n"
	out := roundTrip(t, Frame{Type: TypeConfig, Disabled: []string{"cpu", "mem"}, ConfigYAML: yaml, ConfigRev: 1727172717})
	if out.Type != TypeConfig {
		t.Errorf("type = %q, want %q", out.Type, TypeConfig)
	}
	eqStrings(t, "disabled", out.Disabled, []string{"cpu", "mem"})
	if out.ConfigYAML != yaml {
		t.Errorf("config_yaml = %q, want %q", out.ConfigYAML, yaml)
	}
	if out.ConfigRev != 1727172717 {
		t.Errorf("config_rev = %d, want 1727172717", out.ConfigRev)
	}
}

// A config_state frame (agent → hub) round-trips both the file report form
// (ConfigYAML+ConfigPath) and the apply ack form (ConfigRev+ApplyState+
// ApplyError).
func TestConfigStateFrameRoundTrip(t *testing.T) {
	report := roundTrip(t, Frame{Type: TypeConfigState, ConfigYAML: "# monitor.yaml\n", ConfigPath: "/etc/monitor/monitor.yaml"})
	if report.Type != TypeConfigState {
		t.Errorf("type = %q, want %q", report.Type, TypeConfigState)
	}
	if report.ConfigYAML != "# monitor.yaml\n" {
		t.Errorf("config_yaml = %q, want %q", report.ConfigYAML, "# monitor.yaml\n")
	}
	if report.ConfigPath != "/etc/monitor/monitor.yaml" {
		t.Errorf("config_path = %q, want /etc/monitor/monitor.yaml", report.ConfigPath)
	}

	ack := roundTrip(t, Frame{Type: TypeConfigState, ConfigRev: 42, ApplyState: "rejected", ApplyError: "yaml: line 2: bad indent"})
	if ack.ConfigRev != 42 {
		t.Errorf("config_rev = %d, want 42", ack.ConfigRev)
	}
	if ack.ApplyState != "rejected" {
		t.Errorf("apply_state = %q, want rejected", ack.ApplyState)
	}
	if ack.ApplyError != "yaml: line 2: bad indent" {
		t.Errorf("apply_error = %q, want yaml error text", ack.ApplyError)
	}
}

// Every documented apply outcome state survives the wire unchanged.
func TestConfigStateApplyStatesRoundTrip(t *testing.T) {
	for _, state := range []string{"applied", "rejected", "deferred"} {
		out := roundTrip(t, Frame{Type: TypeConfigState, ConfigRev: 7, ApplyState: state})
		if out.ApplyState != state {
			t.Errorf("apply_state = %q, want %q", out.ApplyState, state)
		}
	}
}

// Empty config fields are omitted: a bare frame does not carry any of the
// config / config_state keys on the wire.
func TestFrameConfigFieldsOmittedWhenEmpty(t *testing.T) {
	b, err := json.Marshal(Frame{Type: TypeConfig})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, key := range []string{"disabled", "config_yaml", "config_rev", "config_path", "apply_state", "apply_error"} {
		if strings.Contains(s, `"`+key+`"`) {
			t.Errorf("wire %s contains key %q for an empty frame", s, key)
		}
	}
	if want := `{"type":"config"}`; s != want {
		t.Errorf("wire = %s, want %s", s, want)
	}
}

// An applied ack with no error carries no apply_error key.
func TestApplyErrorOmittedWhenEmpty(t *testing.T) {
	b, err := json.Marshal(Frame{Type: TypeConfigState, ConfigRev: 9, ApplyState: "applied"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "apply_error") {
		t.Errorf("wire %s should not contain apply_error", string(b))
	}
}
