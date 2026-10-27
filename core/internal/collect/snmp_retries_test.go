package collect

import (
	"slices"
	"testing"
	"time"
)

func TestSNMPRetriesGoToTheCommand(t *testing.T) {
	s := &snmpCollector{}
	if err := s.Configure(func(v any) error {
		*v.(*snmpConfig) = snmpConfig{Address: "10.0.0.5", Community: "public", Version: "2c", Retries: 2}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	args, err := s.snmpArgs("1.3.6.1.2.1.1.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(args[:8], []string{"-v", "2c", "-t", "3", "-r", "2", "-c", "public"}) {
		t.Fatalf("v2c args %v", args)
	}

	v3 := &snmpCollector{}
	if err := v3.Configure(func(v any) error {
		*v.(*snmpConfig) = snmpConfig{Address: "10.0.0.5", Version: "3", User: "monitor", Retries: 1}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	args, err = v3.snmpArgs("1.3.6.1.2.1.1.3.0")
	if err != nil {
		t.Fatal(err)
	}
	idx := slices.Index(args, "-r")
	if idx < 0 || args[idx+1] != "1" {
		t.Fatalf("v3 args %v", args)
	}
	if !slices.Contains(args, "-t") {
		t.Fatalf("v3 args missing timeout: %v", args)
	}

	plain := &snmpCollector{}
	if err := plain.Configure(func(v any) error {
		*v.(*snmpConfig) = snmpConfig{Address: "10.0.0.5", Community: "public"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	args, err = plain.snmpArgs("1.3.6.1.2.1.1.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(args, "-r") {
		t.Fatalf("default args include retries: %v", args)
	}
	if !slices.Equal(args[:4], []string{"-v", "2c", "-t", "3"}) {
		t.Fatalf("default timeout args %v", args)
	}

	bad := &snmpCollector{}
	if err := bad.Configure(func(v any) error {
		*v.(*snmpConfig) = snmpConfig{Address: "10.0.0.5", Community: "public", Retries: 6}
		return nil
	}); err == nil {
		t.Fatal("accepted retries 6")
	}

	parent := &snmpCollector{}
	if err := parent.Configure(func(v any) error {
		*v.(*snmpConfig) = snmpConfig{Retries: 3, Targets: []snmpConfig{{
			Name: "edge", Address: "10.0.0.8", Community: "public",
		}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if parent.cfg.Targets[0].Retries != 3 {
		t.Fatalf("child retries %d", parent.cfg.Targets[0].Retries)
	}
}

func TestSNMPTimeoutPassedToWalk(t *testing.T) {
	if snmpTimeoutSeconds(500*time.Millisecond) != 1 {
		t.Fatalf("500ms -> %d", snmpTimeoutSeconds(500*time.Millisecond))
	}
	if snmpTimeoutSeconds(4500*time.Millisecond) != 5 {
		t.Fatalf("4.5s -> %d", snmpTimeoutSeconds(4500*time.Millisecond))
	}
	s := &snmpCollector{}
	if err := s.Configure(func(v any) error {
		*v.(*snmpConfig) = snmpConfig{Address: "10.0.0.5", Community: "public", Timeout: 4500 * time.Millisecond}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	args, err := s.snmpArgs("1.3.6.1.2.1.1.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(args[:4], []string{"-v", "2c", "-t", "5"}) {
		t.Fatalf("timeout args %v", args)
	}
}
