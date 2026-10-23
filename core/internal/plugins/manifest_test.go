package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func packageFixture(t *testing.T, dir, id string, edit func(map[string]any)) string {
	t.Helper()
	root := filepath.Join(dir, id)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "collector"), []byte("#!/bin/sh\necho DISABLE\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{"api_version": 1, "id": id, "name": "Example", "version": "1.0.0", "protocol": ProtocolV1, "capabilities": []string{"metrics"}, "command": "collector"}
	if edit != nil {
		edit(manifest)
	}
	b, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "plugin.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestManifestDiscoveryAndPrivateExecutionSettings(t *testing.T) {
	dir := t.TempDir()
	packageFixture(t, dir, "example", func(m map[string]any) { m["args"] = []string{"private-fixture-value"} })
	packageFixture(t, dir, "broken", func(m map[string]any) { m["api_version"] = 99 })
	specs, diagnostics := DiscoverManifests(dir)
	if len(specs) != 1 || len(diagnostics) != 1 || diagnostics[0].Path != "broken/plugin.json" {
		t.Fatalf("specs=%d diagnostics=%v", len(specs), diagnostics)
	}
	if specs[0].Source != "manifest" || specs[0].Name != "example" || !filepath.IsAbs(specs[0].Command) || len(specs[0].Args) != 1 {
		t.Fatal("manifest did not create an executable spec")
	}
	b, err := json.Marshal(specs[0].Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "private-fixture-value") || strings.Contains(string(b), "command") || strings.Contains(string(b), "args") {
		t.Fatal("public manifest leaked execution settings")
	}
	if !strings.Contains(string(b), `"version":"1.0.0"`) {
		t.Fatal("public metadata lost")
	}
}

func TestManifestRejectsUnsupportedOrEscapingPackages(t *testing.T) {
	for name, edit := range map[string]func(map[string]any){
		"version":    func(m map[string]any) { m["api_version"] = 2 },
		"protocol":   func(m map[string]any) { m["protocol"] = "rpc/v9" },
		"identity":   func(m map[string]any) { m["id"] = "other" },
		"capability": func(m map[string]any) { m["capabilities"] = []string{"metrics", "shell"} },
		"unknown":    func(m map[string]any) { m["remote_url"] = "https://example.invalid" },
		"traversal":  func(m map[string]any) { m["command"] = "../outside" },
		"absolute":   func(m map[string]any) { m["command"] = "/bin/sh" },
		"missing":    func(m map[string]any) { m["command"] = "missing" },
		"timeout":    func(m map[string]any) { m["timeout"] = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			packageFixture(t, dir, "example", edit)
			specs, diagnostics := DiscoverManifests(dir)
			if len(specs) != 0 || len(diagnostics) != 1 {
				t.Fatalf("invalid package accepted: specs=%d diagnostics=%v", len(specs), diagnostics)
			}
		})
	}
}

func TestManifestSymlinksAndOversizeFailClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires host privileges")
	}
	for _, target := range []string{"manifest", "command", "package", "oversize"} {
		t.Run(target, func(t *testing.T) {
			dir := t.TempDir()
			path := packageFixture(t, dir, "example", nil)
			switch target {
			case "oversize":
				if err := os.WriteFile(path, []byte(strings.Repeat(" ", 65537)), 0o600); err != nil {
					t.Fatal(err)
				}
			default:
				if target == "command" {
					path = filepath.Join(dir, "example", "collector")
				}
				if target == "package" {
					path = filepath.Dir(path)
				}
				moved := filepath.Join(t.TempDir(), "moved")
				if err := os.Rename(path, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, path); err != nil {
					t.Fatal(err)
				}
			}
			specs, diagnostics := DiscoverManifests(dir)
			if len(specs) != 0 || len(diagnostics) == 0 {
				t.Fatal("unsafe package was not rejected")
			}
		})
	}
}

func TestPackageMetricsPolicyAndBudgetsSurviveRestart(t *testing.T) {
	reg := newReg(&memSink{})
	reg.AddChart(&registry.Chart{ID: "system.cpu", Plugin: "cpu", Dimensions: []*registry.Dimension{{ID: "user"}}})
	spec := Spec{Name: "example", Manifest: &Manifest{ID: "example"}}
	p := newPluginParser(spec, reg)
	p.MaxCharts, p.MaxDimensions = 1, 1
	for _, line := range []string{"CHART system.cpu", "OVERWRITE system.cpu", "CHART plugin.other.load", "HOST_LABEL owner hacked", "FUNCTION run 10"} {
		if err := p.Line(line); err == nil {
			t.Fatalf("accepted forbidden command %q", line)
		}
	}
	if err := p.Run(strings.NewReader("CHART plugin.example.load\nDIMENSION value\nBEGIN plugin.example.load\nSET value = 7\nEND\n")); err != nil {
		t.Fatal(err)
	}
	c, ok := reg.Chart("plugin.example.load")
	if !ok {
		t.Fatal("valid package chart absent")
	}
	_, values := c.LastValues()
	if values["value"] != 7 {
		t.Fatalf("sample=%v", values)
	}
	if err := p.Line("CHART plugin.example.extra"); err == nil {
		t.Fatal("chart budget bypassed")
	}
	p = newPluginParser(spec, reg)
	p.MaxCharts, p.MaxDimensions = 1, 1
	if err := p.Line("CHART plugin.example.extra"); err == nil {
		t.Fatal("restart bypassed chart budget")
	}
	if err := p.Line("OVERWRITE plugin.example.load"); err == nil {
		t.Fatal("overwrite could discard dimension history and bypass the cumulative budget")
	}
	if err := p.Line("CHART plugin.example.load"); err != nil {
		t.Fatal(err)
	}
	if err := p.Line("DIMENSION extra"); err == nil {
		t.Fatal("restart bypassed dimension budget")
	}
	if err := p.Line("DIMENSION value"); err != nil {
		t.Fatal(err)
	}
	if err := p.Line("BEGIN plugin.example.load"); err != nil {
		t.Fatal(err)
	}
	if err := p.Line("SET value = NaN"); err == nil {
		t.Fatal("accepted non-finite sample")
	}
	if err := p.Line("VARIABLE invalid = +Inf"); err == nil {
		t.Fatal("accepted non-finite variable")
	}
	if c, _ := reg.Chart("system.cpu"); c.Plugin != "cpu" || c.Dimension("user") == nil {
		t.Fatal("builtin chart mutated")
	}
	if len(reg.Host.Labels) != 0 {
		t.Fatal("host labels mutated")
	}
}

func TestPackageCannotAdoptLegacyChartInsideItsNamespace(t *testing.T) {
	reg := newReg(nil)
	reg.AddChart(&registry.Chart{ID: "plugin.example.owned", Plugin: "legacy"})
	p := newPluginParser(Spec{Name: "example", Manifest: &Manifest{ID: "example"}}, reg)
	if err := p.Line("OVERWRITE plugin.example.owned"); err == nil {
		t.Fatal("package adopted a legacy chart")
	}
}
