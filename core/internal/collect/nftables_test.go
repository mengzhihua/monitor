package collect

import (
	"context"
	"encoding/binary"
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

func TestParseNftNetlink(t *testing.T) {
	nla := func(typ uint16, val []byte) []byte {
		al := 4 + len(val)
		b := make([]byte, al)
		binary.BigEndian.PutUint16(b[0:2], uint16(al))
		binary.BigEndian.PutUint16(b[2:4], typ)
		copy(b[4:], val)
		if pad := (4 - al%4) % 4; pad > 0 {
			b = append(b, make([]byte, pad)...)
		}
		return b
	}
	u32 := func(v uint32) []byte {
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, v)
		return b
	}
	u64 := func(v uint64) []byte {
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, v)
		return b
	}
	inner := append(nla(1, u64(345)), nla(2, u64(12))...)
	obj := append(nla(1, append([]byte("filter"), 0)), nla(2, append([]byte("http"), 0))...)
	obj = append(obj, nla(3, u32(1))...)
	obj = append(obj, nla(4, inner)...)
	payload := append([]byte{0, 0, 0, 0}, obj...)
	msg := make([]byte, 16)
	binary.LittleEndian.PutUint32(msg[0:4], uint32(16+len(payload)))
	binary.LittleEndian.PutUint16(msg[4:6], (10<<8)|19)
	msg = append(msg, payload...)
	got := parseNftNetlink(msg)
	if len(got) != 1 || got[0].Table != "filter" || got[0].Name != "http" || got[0].Packets != 12 || got[0].Bytes != 345 {
		t.Fatalf("%+v", got)
	}
}
