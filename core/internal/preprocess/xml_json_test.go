package preprocess

import (
	"math"
	"strings"
	"testing"
)

func TestXMLToJSON(t *testing.T) {
	doc := "<host><name>web</name><status>1</status></host>"
	v, _, err := Apply([]Step{{Type: "xml_to_json"}, {Type: "jsonpath", Path: "$.host.status"}}, doc, math.NaN())
	if err != nil || v != 1 {
		t.Fatalf("status %v %v", v, err)
	}
	rows := "<rows><row><n>1</n></row><row><n>4</n></row></rows>"
	v, _, err = Apply([]Step{{Type: "xml_to_json"}, {Type: "jsonpath", Path: "$.rows.row[1].n"}}, rows, math.NaN())
	if err != nil || v != 4 {
		t.Fatalf("repeated %v %v", v, err)
	}
	v, _, err = Apply([]Step{{Type: "xml_to_json"}, {Type: "jsonpath", Path: "$.item.#text"}}, `<item id="3">9</item>`, math.NaN())
	if err != nil || v != 9 {
		t.Fatalf("text %v %v", v, err)
	}
	raw, err := applyXMLToJSON(`<item id="3"/>`)
	if err != nil || !strings.Contains(raw, `"@id":"3"`) {
		t.Fatalf("attribute %q %v", raw, err)
	}
	if _, err = applyXMLToJSON(`<!DOCTYPE foo [<!ENTITY x "y">]><foo>&x;</foo>`); err == nil {
		t.Fatal("accepted a doctype")
	}
	if _, err = applyXMLToJSON("not xml"); err == nil {
		t.Fatal("accepted non-xml")
	}
	deep := "<a>" + strings.Repeat("<a>", maxXMLDepth) + strings.Repeat("</a>", maxXMLDepth) + "</a>"
	if _, err = applyXMLToJSON(deep); err == nil {
		t.Fatal("accepted a document past the depth limit")
	}
}
