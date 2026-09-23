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
