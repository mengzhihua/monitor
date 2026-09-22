package collect

import (
	"context"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestParseXLList(t *testing.T) {
	doms := parseXLList("Name                                        ID   Mem VCPUs	State	Time(s)\nDomain-0                                     0  1024     4     r-----     12.5\nguest                                        1   512     2     -b----      3.0\n")
	if len(doms) != 2 || doms[0].Name != "Domain-0" || doms[0].Mem != 1024 || doms[0].Time != 12.5 || doms[1].VCPUs != 2 {
		t.Fatalf("%+v", doms)
	}
}

func TestXenstatCollectorFixture(t *testing.T) {
	x := &xenstatCollector{}
	x.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("Name ID Mem VCPUs State Time(s)\nDomain-0 0 1024 4 r----- 10.0\n"), nil
	}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := x.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := x.Collect(context.Background(), reg, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("xen.cpu.Domain-0"); !ok {
		t.Fatal("missing cpu")
	}
	ch, _ := reg.Chart("xen.state.Domain-0")
	if ch.Context != "xen.state" {
		t.Fatalf("context %q", ch.Context)
	}
	_, vals := ch.LastValues()
	if vals["running"] != 1 {
		t.Fatalf("%v", vals)
	}
}
