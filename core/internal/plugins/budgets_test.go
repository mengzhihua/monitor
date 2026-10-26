package plugins

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConfiguredPackageBudgetReplacesBuiltinCaps(t *testing.T) {
	reg := newReg(&memSink{})
	spec := Spec{Name: "example", Manifest: &Manifest{ID: "example"}}
	p := newPluginParser(spec, reg, PackageBudgets{MaxCharts: 1, MaxDimensions: 1, MaxLabels: 1, MaxVariables: 1, MaxLine: 2048})
	if p.MaxLine != 2048 || p.MaxCharts != 1 || p.MaxLabels != 1 || p.MaxVariables != 1 {
		t.Fatalf("parser limits = %+v", p)
	}
	body := "CHART plugin.example.load\nDIMENSION value\nVARIABLE first = 1\nBEGIN plugin.example.load\nSET value = 1\nEND\n"
	if err := p.Run(strings.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if err := p.Line("CHART plugin.example.extra"); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("chart budget: %v", err)
	}
	if err := p.Line("CHART plugin.example.load"); err != nil {
		t.Fatal(err)
	}
	if err := p.Line("DIMENSION extra"); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("dimension budget: %v", err)
	}
	if err := p.Line("CLABEL owner one"); err != nil {
		t.Fatal(err)
	}
	if err := p.Line("CLABEL other two"); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("label budget: %v", err)
	}
	if err := p.Line("VARIABLE second = 2"); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("variable budget: %v", err)
	}
}

func TestLegacyPluginsIgnorePackageChartBudget(t *testing.T) {
	p := newPluginParser(Spec{Name: "legacy"}, newReg(&memSink{}), PackageBudgets{MaxCharts: 1})
	if p.Namespace != "" || p.MaxCharts != 0 || p.MaxLine != 64<<10 {
		t.Fatalf("legacy parser %+v", p)
	}
	if err := p.Line("CHART system.cpu"); err != nil {
		t.Fatal(err)
	}
	if err := p.Line("CHART system.load"); err != nil {
		t.Fatal(err)
	}
}

func TestManifestResourcesCannotRaiseTheProcessBudget(t *testing.T) {
	dir := t.TempDir()
	packageFixture(t, dir, "example", func(m map[string]any) {
		m["resources"] = map[string]any{"max_charts": 2, "max_line": 4096}
	})
	specs, diagnostics := DiscoverManifests(dir)
	if len(diagnostics) != 0 || len(specs) != 1 || specs[0].MaxCharts != 2 || specs[0].MaxLine != 4096 {
		t.Fatalf("specs=%+v diagnostics=%v", specs, diagnostics)
	}
	public, err := json.Marshal(specs[0].Manifest)
	if err != nil || !strings.Contains(string(public), `"max_charts":2`) || strings.Contains(string(public), `"command"`) {
		t.Fatalf("public manifest %s %v", public, err)
	}
	reg := newReg(&memSink{})
	p := newPluginParser(specs[0], reg, PackageBudgets{})
	if p.MaxCharts != 2 || p.MaxDimensions != 64 || p.MaxLine != 4096 {
		t.Fatalf("tightened parser %+v", p)
	}
	if err := p.Line("CHART plugin.example.one"); err != nil {
		t.Fatal(err)
	}
	if err := p.Line("CHART plugin.example.two"); err != nil {
		t.Fatal(err)
	}
	if err := p.Line("CHART plugin.example.three"); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("package ceiling: %v", err)
	}
	clamped := newPluginParser(specs[0], newReg(&memSink{}), PackageBudgets{MaxCharts: 1})
	if clamped.MaxCharts != 1 || clamped.MaxLine != 4096 {
		t.Fatalf("process ceiling lost %+v", clamped)
	}
	bad := t.TempDir()
	packageFixture(t, bad, "example", func(m map[string]any) {
		m["resources"] = map[string]any{"max_charts": 10001}
	})
	if specs, diagnostics = DiscoverManifests(bad); len(specs) != 0 || len(diagnostics) != 1 {
		t.Fatalf("oversized resource accepted: %+v %+v", specs, diagnostics)
	}
}

func TestConfiguredScriptCeilingCountsItsOwnCharts(t *testing.T) {
	p := newPluginParser(Spec{Name: "legacy", MaxCharts: 1}, newReg(&memSink{}), PackageBudgets{})
	if p.Namespace != "" || p.MaxCharts != 1 {
		t.Fatalf("%+v", p)
	}
	if err := p.Line("CHART local.one"); err != nil {
		t.Fatal(err)
	}
	if err := p.Line("CHART local.two"); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("configured ceiling: %v", err)
	}
}

func TestCatalogReportsPerPluginCeiling(t *testing.T) {
	m := New(newReg(&memSink{}), []Spec{{Name: "example", Command: "example", Manifest: &Manifest{ID: "example"}, MaxCharts: 2}}, Options{})
	plugins := m.Catalog().Plugins
	if len(plugins) != 1 || plugins[0].Budgets == nil || plugins[0].Budgets.MaxCharts != 2 || plugins[0].Budgets.MaxDimensions != 64 {
		t.Fatalf("%+v", plugins)
	}
	if m.Catalog().Budgets.MaxCharts != 128 {
		t.Fatal("process ceiling changed")
	}
}

func TestCatalogReportsEffectivePackageBudgets(t *testing.T) {
	m := New(newReg(&memSink{}), nil, Options{MaxCharts: 3, MaxDimensions: 2, MaxLine: 2048})
	b := m.Catalog().Budgets
	if b.MaxCharts != 3 || b.MaxDimensions != 2 || b.MaxLabels != 128 || b.MaxVariables != 128 || b.MaxLine != 2048 {
		t.Fatalf("%+v", b)
	}
	defaults := New(newReg(&memSink{}), nil, Options{}).Catalog().Budgets
	if defaults != (PackageBudgets{MaxCharts: 128, MaxDimensions: 64, MaxLabels: 128, MaxVariables: 128, MaxLine: 64 << 10}) {
		t.Fatalf("defaults %+v", defaults)
	}
}
