package collect

import (
	"context"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestParseNftCounters(t *testing.T) {
	raw := "table ip filter {\n        counter http {\n                packets 12 bytes 345\n        }\n        counter dns {\n                packets 1 bytes 2\n        }\n}\n"
	cs := parseNftCounters(raw)
	if len(cs) != 2 || cs[0].Name != "http" || cs[0].Packets != 12 || cs[0].Bytes != 345 || cs[1].Name != "dns" {
		t.Fatalf("%+v", cs)
	}
}

func TestNftablesCollectorFixture(t *testing.T) {
	n := &nftablesCollector{}
	n.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("table ip filter {\n counter web {\n packets 5 bytes 50\n }\n}\n"), nil
	}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := n.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := n.Collect(context.Background(), reg, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("netfilter.nftables_packets.filter_web"); !ok {
		t.Fatal("missing packets chart")
	}
}
