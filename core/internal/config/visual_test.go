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

func TestApplyVisualUpdatesCollectorEndpointOnly(t *testing.T) {
	raw := "collectors:\n  modules:\n    nginx:\n      url: http://old/stub_status\n      timeout: 2s\n    mysql:\n      address: 127.0.0.1:3306\n      password: secret\n"
	v, err := VisualFrom(raw)
	if err != nil {
		t.Fatal(err)
	}
	var nginx *VisualTarget
	for i := range v.Targets {
		if v.Targets[i].Name == "nginx" {
			nginx = &v.Targets[i]
		}
		if v.Targets[i].Name == "mysql" && v.Targets[i].Address != "127.0.0.1:3306" {
			t.Fatalf("mysql = %+v", v.Targets[i])
		}
	}
	if nginx == nil || nginx.URL != "http://old/stub_status" {
		t.Fatalf("nginx missing: %+v", v.Targets)
	}
	nginx.URL = "http://127.0.0.1/stub_status"
	next, err := ApplyVisual(raw, v)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(next, "http://127.0.0.1/stub_status") || !strings.Contains(next, "timeout: 2s") || !strings.Contains(next, "password: secret") {
		t.Fatalf("module fields lost:\n%s", next)
	}
}

func TestApplyVisualKeepsNotifySecretsOutsideTheForm(t *testing.T) {
	raw := "health:\n  notify:\n    webhook:\n      url: http://127.0.0.1/hook\n      headers:\n        X-Token: keep-me\n    feishu:\n      webhook_url_env: MONITOR_FEISHU_WEBHOOK\n      secret_env: MONITOR_FEISHU_SECRET\n    email:\n      password_env: MAIL_PASSWORD\n    roles:\n      sysadmin: [slack, webhook]\n"
	v, err := VisualFrom(raw)
	if err != nil {
		t.Fatal(err)
	}
	if v.Notify.FeishuWebhookURLEnv != "MONITOR_FEISHU_WEBHOOK" || len(v.Notify.Roles) != 1 || v.Notify.Roles[0].Name != "sysadmin" {
		t.Fatalf("notify = %+v", v.Notify)
	}
	v.Notify.SlackWebhookURL = "https://hooks.example/slack"
	next, err := ApplyVisual(raw, v)
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"keep-me", "MONITOR_FEISHU_WEBHOOK", "MONITOR_FEISHU_SECRET", "MAIL_PASSWORD", "sysadmin", "https://hooks.example/slack"} {
		if !strings.Contains(next, keep) {
			t.Fatalf("missing %s\n%s", keep, next)
		}
	}
}

func TestApplyVisualOmitsEmptyNotifyChannels(t *testing.T) {
	raw := "global:\n  hostname: old\n"
	v, err := VisualFrom(raw)
	if err != nil {
		t.Fatal(err)
	}
	v.Hostname = "new-host"
	next, err := ApplyVisual(raw, v)
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"dingtalk:", "webhook:", "slack:", "notify:"} {
		if strings.Contains(next, absent) {
			t.Fatalf("empty notify channel %s written:\n%s", absent, next)
		}
	}
	if !strings.Contains(next, "new-host") {
		t.Fatalf("hostname missing:\n%s", next)
	}
}

func LoadString(raw string) (*Config, error) {
	c := Default()
	if err := yaml.Unmarshal([]byte(raw), c); err != nil {
		return nil, err
	}
	return c, nil
}
