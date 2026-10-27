package collect

import (
	"context"
	"errors"
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

func TestIPMIReadFailureMarksUnsupported(t *testing.T) {
	phase := "init"
	i := &ipmiCollector{}
	i.run = func(context.Context, string, ...string) ([]byte, error) {
		switch phase {
		case "fail":
			return nil, errors.New("ipmitool: no response from 192.0.2.10")
		case "ok":
			return []byte("CPU Temp | 55 degrees C | ok\n"), nil
		default:
			return []byte("CPU Temp | 40 degrees C | ok\n"), nil
		}
	}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := i.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	phase = "fail"
	if err := i.Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	state := mustChart(t, reg, "ipmi.sensor_state")
	if state.Unsupported == "" || state.LastCollectionTime() != 0 {
		t.Fatalf("unsupported %q at %d", state.Unsupported, state.LastCollectionTime())
	}
	if err := i.Collect(context.Background(), reg, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if state.Unsupported == "" {
		t.Fatal("skipped tick cleared the failure")
	}
	phase = "ok"
	if err := i.Collect(context.Background(), reg, now.Add(20*time.Second)); err != nil {
		t.Fatal(err)
	}
	if state.Unsupported != "" {
		t.Fatalf("failure stuck: %q", state.Unsupported)
	}
	found := false
	for _, ch := range reg.Charts() {
		if strings.HasPrefix(ch.ID, "ipmi.temperatures.") {
			found = true
			_, vals := ch.LastValues()
			if vals["value"] != 55 {
				t.Fatalf("%s %v", ch.ID, vals)
			}
		}
	}
	if !found {
		t.Fatal("missing recovered temperature")
	}
}
