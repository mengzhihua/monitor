package collect

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestParseIPMISDR(t *testing.T) {
	s := parseIPMISDR("CPU Temp         | 45 degrees C      | ok\nFAN1             | 1200 RPM          | ok\n12V              | 12.1 Volts        | nc\nBad              | no reading        | ns\n")
	if len(s) != 4 || s[0].Kind != "temperatures" || s[0].Value != 45 || s[1].Kind != "fans" || s[2].State != "nc" {
		t.Fatalf("%+v", s)
	}
}

func TestIPMICollectorFixture(t *testing.T) {
	i := &ipmiCollector{}
	i.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("CPU Temp | 40 degrees C | ok\n"), nil
	}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := i.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := i.Collect(context.Background(), reg, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ch := range reg.Charts() {
		if strings.HasPrefix(ch.ID, "ipmi.temperatures.") {
			found = true
			_, vals := ch.LastValues()
			if vals["value"] != 40 {
				t.Fatalf("%s %v", ch.ID, vals)
			}
			if ch.Context != "ipmi.temperatures" {
				t.Fatalf("context %q", ch.Context)
			}
		}
	}
	if !found {
		t.Fatal("missing temp")
	}
}
