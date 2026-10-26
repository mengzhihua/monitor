package plugins

import (
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
