package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWebEnabledDefaultAndOverride(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "monitor.yaml")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	c, err := Load(write("mode: agent\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.WebEnabled() {
		t.Fatal("web must stay enabled when web.enabled is unset")
	}

	c, err = Load(write("mode: agent\nweb:\n  enabled: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.WebEnabled() {
		t.Fatal("web.enabled=false must disable the server")
	}

	c, err = Load(write("mode: agent\nweb:\n  enabled: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.WebEnabled() {
		t.Fatal("explicit web.enabled=true must keep the server on")
	}
}

func TestNotificationSecretEnvironmentReferences(t *testing.T) {
	t.Setenv("MONITOR_TEST_MAIL_PASSWORD", "smtp-private-fixture")
	t.Setenv("MONITOR_TEST_FEISHU_URL", "https://example.invalid/private-fixture")
	t.Setenv("MONITOR_TEST_FEISHU_SECRET", "signing-private-fixture")
	path := filepath.Join(t.TempDir(), "notify.yaml")
	body := "health:\n  notify:\n    email:\n      password: old\n      password_env: MONITOR_TEST_MAIL_PASSWORD\n      tls_mode: starttls\n    feishu:\n      webhook_url_env: MONITOR_TEST_FEISHU_URL\n      secret_env: MONITOR_TEST_FEISHU_SECRET\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Health.Notify.Email.Password != "smtp-private-fixture" || c.Health.Notify.Feishu.Secret != "signing-private-fixture" || c.Health.Notify.Feishu.WebhookURL != "https://example.invalid/private-fixture" || c.Health.Notify.Email.TLSMode != "starttls" {
		t.Fatal("environment fields not resolved")
	}
	t.Setenv("MONITOR_TEST_FEISHU_SECRET", "")
	if _, err := Load(path); err == nil {
		t.Fatal("empty secret environment accepted")
	}
}

func TestPushServiceEnvironmentReferences(t *testing.T) {
	for _, key := range []string{"PUSH_TOPIC", "PUSH_TOKEN", "GOTIFY_TOKEN", "BARK_KEY"} {
		t.Setenv(key, "private-fixture")
	}
	path := filepath.Join(t.TempDir(), "notify.yaml")
	body := "health:\n  notify:\n    ntfy:\n      topic_env: PUSH_TOPIC\n      token_env: PUSH_TOKEN\n    gotify:\n      token_env: GOTIFY_TOKEN\n    bark:\n      device_key_env: BARK_KEY\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	n := c.Health.Notify
	if n.Ntfy.Topic != "private-fixture" || n.Ntfy.Token != "private-fixture" || n.Gotify.Token != "private-fixture" || n.Bark.DeviceKey != "private-fixture" {
		t.Fatal("push secret references not resolved")
	}
	for _, key := range []string{"PUSH_TOPIC", "PUSH_TOKEN", "GOTIFY_TOKEN", "BARK_KEY"} {
		t.Setenv(key, "")
		if _, err := Load(path); err == nil {
			t.Fatal("empty referenced variable accepted")
		}
		t.Setenv(key, "private-fixture")
	}
}
