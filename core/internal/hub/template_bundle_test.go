package hub

import (
	"strings"
	"testing"

	"github.com/mengzhihua/monitor/core/internal/health"
)

func TestTemplateBundleRoundTripAndCreateMode(t *testing.T) {
	src, err := OpenTemplates(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	saved, err := src.Upsert(Template{
		Name: "linux", Macros: map[string]string{"T": "1"},
		Rules: []health.RuleSpec{{Name: "hot", On: "system.cpu", Calc: "$user", Warn: "$this > {$T}", Every: "1s"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := src.Export(nil)
	if err != nil || len(bundle.Templates) != 1 || bundle.Templates[0].Updated != 0 || bundle.Templates[0].ID != saved.ID {
		t.Fatalf("%+v %v", bundle, err)
	}
	raw, err := EncodeBundle(bundle, "yaml")
	if err != nil || !strings.Contains(string(raw), "linux") {
		t.Fatalf("%s %v", raw, err)
	}
	decoded, err := DecodeBundle(raw)
	if err != nil {
		t.Fatal(err)
	}
	dst, err := OpenTemplates(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	imported, err := dst.Import(decoded, "create")
	if err != nil || len(imported) != 1 || imported[0].Name != "linux" || imported[0].Macros["T"] != "1" {
		t.Fatalf("%+v %v", imported, err)
	}
	decoded.Templates[0].Macros["T"] = "9"
	if _, err := dst.Import(decoded, "create"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatal(err)
	}
	updated, err := dst.Import(decoded, "update")
	if err != nil || updated[0].ID != imported[0].ID || updated[0].Macros["T"] != "9" {
		t.Fatalf("%+v %v", updated, err)
	}
	if list := dst.List(); len(list) != 1 {
		t.Fatalf("import duplicated templates: %+v", list)
	}
}

func TestTemplateBundleRejectsInvalidRuleWithoutWriting(t *testing.T) {
	dst, err := OpenTemplates(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dst.Upsert(Template{Name: "keep"}, nil); err != nil {
		t.Fatal(err)
	}
	_, err = dst.Import(TemplateBundle{Version: 1, Templates: []Template{{
		Name: "bad", Rules: []health.RuleSpec{{Name: "x"}},
	}}}, "create")
	if err == nil {
		t.Fatal("accepted a rule without a chart")
	}
	if list := dst.List(); len(list) != 1 || list[0].Name != "keep" {
		t.Fatalf("%+v", list)
	}
}
