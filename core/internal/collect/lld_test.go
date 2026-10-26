package collect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"gopkg.in/yaml.v3"
)

func TestLLDPrototypesCreateItemAndPieCharts(t *testing.T) {
	body := `{"data":[{"{#NAME}":"sda","{#USED}":"25"},{"{#NAME}":"sdb","{#USED}":"50"},{"{#NAME}":"bad","{#USED}":"1+1"},{"{#NAME}":"sdc","{#USED}":"75"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	raw := "rules:\n" +
		"  - name: disks\n" +
		"    url: " + server.URL + "\n" +
		"    prototypes:\n" +
		"      - {name: used, title: \"{#NAME} used\", key: \"{#NAME}\", value: \"{#USED}\", units: \"%\"}\n" +
		"      - {name: share, title: \"{#NAME}\", key: \"{#NAME}\", value: \"{#USED}\", units: \"%\", type: pie, layout: graph, filter_macro: \"{#NAME}\", filter: \"^sd[ac]$\"}\n"
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	c := &lldCollector{}
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	if err := c.Configure(node.Decode); err != nil {
		t.Fatal(err)
	}
	if err := c.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Collect(context.Background(), reg, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	item, ok := reg.Chart("lld.disks.used.sda")
	if !ok {
		t.Fatal("item chart missing")
	}
	_, vals := item.LastValues()
	if vals["value"] != 25 || item.Title != "sda used" {
		t.Fatalf("item chart %+v vals %v", item.Title, vals)
	}
	if _, ok := reg.Chart("lld.disks.used.sdb"); !ok {
		t.Fatal("numeric entity missing")
	}
	if _, ok := reg.Chart("lld.disks.used.bad"); ok {
		t.Fatal("expression text was treated as a value")
	}
	pie, ok := reg.Chart("lld.disks.share")
	if !ok || pie.Type != registry.Pie {
		t.Fatalf("pie chart %#v", pie)
	}
	_, pieVals := pie.LastValues()
	if pieVals["sda"] != 25 || pieVals["sdc"] != 75 {
		t.Fatalf("pie values %v", pieVals)
	}
	if _, ok := pieVals["sdb"]; ok {
		t.Fatal("filter kept sdb")
	}
}

func TestLLDRejectsBadPrototype(t *testing.T) {
	c := &lldCollector{}
	var node yaml.Node
	raw := "rules:\n  - {name: disks, data: '[]', prototypes: [{name: used, key: '{#NAME}', value: 'system(\"rm\")', type: script}]}\n"
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	if err := c.Configure(node.Decode); err == nil {
		t.Fatal("accepted an unknown chart type")
	}
}
