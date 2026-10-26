package collect

import (
	"slices"
	"testing"
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
	if !slices.Equal(args[:6], []string{"-v", "2c", "-r", "2", "-c", "public"}) {
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
	if !slices.Contains(args, "-r") || args[3] != "1" {
		t.Fatalf("v3 args %v", args)
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
