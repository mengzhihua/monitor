package config

import (
	"strings"
	"testing"
)

func TestMergeYAMLKeepsUntouchedKeys(t *testing.T) {
	base := "global:\n  hostname: old\n  update_every: 1\nweb:\n  token: secret\n"
	got, err := MergeYAML(base, "global:\n  hostname: next\n")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse([]byte(got))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Global.Hostname != "next" || cfg.Global.UpdateEvery != 1 || cfg.Web.Token != "secret" {
		t.Fatalf("merged %#v\n%s", cfg.Global, got)
	}
	again, err := MergeYAML(got, "web:\n  listen: 127.0.0.1:19999\n")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = Parse([]byte(again))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Global.Hostname != "next" || cfg.Web.Token != "secret" || !strings.Contains(again, "19999") {
		t.Fatalf("second patch dropped fields:\n%s", again)
	}
}

func TestMergeYAMLRejectsNonMapping(t *testing.T) {
	if _, err := MergeYAML("global:\n  hostname: a\n", "- 1\n"); err == nil {
		t.Fatal("sequence patch accepted")
	}
}
