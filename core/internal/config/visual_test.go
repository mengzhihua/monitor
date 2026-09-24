package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestApplyVisualKeepsUntouchedConfig(t *testing.T) {
	raw := "global:\n  hostname: old\nweb:\n  token: secret-token\n  listen: \":19999\"\ncollectors:\n  enabled: [cpu]\n  modules:\n    nginx:\n      url: http://127.0.0.1/stub_status\nhealth:\n  alarms:\n    - name: ram_notice\n      on: system.ram\n"
	v, err := VisualFrom(raw)
	if err != nil {
		t.Fatal(err)
	}
	if v.Hostname != "old" || v.Listen != ":19999" || len(v.CollectorsEnabled) != 1 {
		t.Fatalf("form = %+v", v)
	}
	v.Hostname = "new-host"
	v.Users = []VisualUser{{Name: "ops", Token: "tok-1", Role: "admin"}}
	next, err := ApplyVisual(raw, v)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(next, "secret-token") || !strings.Contains(next, "stub_status") || !strings.Contains(next, "ram_notice") {
		t.Fatalf("dropped untouched config:\n%s", next)
	}
	if !strings.Contains(next, "new-host") || !strings.Contains(next, "tok-1") {
		t.Fatalf("form fields missing:\n%s", next)
	}
	cfg, err := LoadString(next)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Global.Hostname != "new-host" || cfg.Web.Token != "secret-token" || len(cfg.Health.Alarms) != 1 {
		t.Fatalf("loaded = hostname %q token %q alarms %d", cfg.Global.Hostname, cfg.Web.Token, len(cfg.Health.Alarms))
	}
	if _, err := ApplyVisual(raw, Visual{Mode: "nope", UpdateEvery: 1, WebEnabled: "default", HealthEnabled: "default"}); err == nil {
		t.Fatal("invalid mode accepted")
	}
}

func LoadString(raw string) (*Config, error) {
	c := Default()
	if err := yaml.Unmarshal([]byte(raw), c); err != nil {
		return nil, err
	}
	return c, nil
}
