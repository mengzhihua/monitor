package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/mengzhihua/monitor/core/internal/config"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestHealthStartupRestoresRuleOverridesOverCurrentConfiguration(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.Global.DataDir = filepath.Join(root, "data")
	path := filepath.Join(root, "monitor.yaml")
	if err := os.Mkdir(filepath.Join(root, "health.d"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "health.d", "custom.yaml"), []byte("alarms:\n  - name: 10min_cpu_usage\n    on: system.cpu\n    calc: '2'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	base := health.RuleSpec{Name: "10min_cpu_usage", On: "system.cpu", Calc: "3", Info: "inline definition"}
	cfg.Health.Alarms = []health.RuleSpec{base}
	reg := registry.New(&registry.Host{Hostname: "startup-fixture", UpdateEvery: 1}, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	open := func() *health.Engine {
		t.Helper()
		e, err := newHealth(cfg, path, reg, nil, logger)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(e.Close)
		return e
	}
	find := func(rules []*health.Rule, name string) *health.Rule {
		t.Helper()
		for _, rule := range rules {
			if rule.Spec.Name == name {
				return rule
			}
		}
		t.Fatalf("missing rule %s", name)
		return nil
	}
	e := open()
	if current := find(e.RulesConfig().Base, base.Name); current.Spec.Calc != "3" || current.Source != path {
		t.Fatalf("base ignored builtin/file/inline precedence: %+v", current)
	}
	custom := base
	custom.Calc = "4"
	saved, err := e.MutateRules(nil, health.RuleMutation{Upserts: []health.RuleSpec{custom}, Delete: []string{"10min_cpu_iowait"}})
	if err != nil {
		t.Fatal(err)
	}
	e.Close()
	if _, err := os.Stat(filepath.Join(cfg.Global.DataDir, "health", "alert-rules.json")); err != nil {
		t.Fatalf("rules were not saved in this agent's data directory: %v", err)
	}
	cfg.Health.Alarms[0].Calc = "5"
	reopened := open()
	snapshot := reopened.RulesConfig()
	if snapshot.Revision == saved.Revision || find(snapshot.Rules, base.Name).Spec.Calc != "4" || len(snapshot.Removed) != 1 {
		t.Fatal("startup lost overlay/tombstone or reused prior revision")
	}
	restored, err := reopened.MutateRules(&snapshot.Revision, health.RuleMutation{Restore: []string{base.Name, "10min_cpu_iowait"}})
	if err != nil {
		t.Fatal(err)
	}
	if current := find(restored.Rules, base.Name); current.Spec.Calc != "5" || current.Source != path || len(restored.Removed) != 0 {
		t.Fatalf("restore ignored current configuration: %+v", current)
	}
}
