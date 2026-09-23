package mobile

import "strings"
import "testing"

func TestYAML(t *testing.T) {
	y := YAML("/data", 19999, "http://hub", "k", false)
	if !strings.Contains(y, `mode: agent`) || !strings.Contains(y, `"http://hub"`) {
		t.Fatal(y)
	}
	h := YAML("/data", 19999, "", "", true)
	if !strings.Contains(h, "mode: hub") || strings.Contains(h, "destinations") {
		t.Fatal(h)
	}
}
