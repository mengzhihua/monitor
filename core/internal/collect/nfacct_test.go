package collect

import (
	"context"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestParseNfacct(t *testing.T) {
	raw := `{ pkts = 00000000000000000100, bytes = 00000000000000102400 } = http;
{ pkts = 3, bytes = 30 } = ssh;
`
	os := parseNfacct(raw)
	if len(os) != 2 || os[0].Name != "http" || os[0].Packets != 100 || os[0].Bytes != 102400 || os[1].Name != "ssh" {
		t.Fatalf("%+v", os)
	}
}

func TestNfacctCollectorFixture(t *testing.T) {
	n := &nfacctCollector{}
	n.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("{ pkts = 5, bytes = 50 } = web;\n"), nil
	}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := n.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := n.Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("netfilter.nfacct_packets.web"); !ok {
		t.Fatal("missing packets chart")
	}
	if _, ok := reg.Chart("netfilter.nfacct_bytes.web"); !ok {
		t.Fatal("missing bytes chart")
	}
}
