package collect

import (
	"context"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestParseBpftoolProg(t *testing.T) {
	raw := `1: kprobe  name sys_open  tag abcdef  gpl
	loaded_at 2024-01-01T00:00:00+0000  uid 0
	xlated 296B  jited 180B  memlock 4096B  map_ids 2
	run_time_ns 12345  run_cnt 10
2: xdp  name drop  tag 111
	memlock 2048B
`
	ps := parseBpftoolProg(raw)
	if len(ps) != 2 || ps[0].Name != "sys_open" || ps[0].Memlock != 4096 || ps[0].RunCnt != 10 || ps[1].Type != "xdp" {
		t.Fatalf("%+v", ps)
	}
	if parseByteSize("2KiB") != 2048 || parseByteSize("1M") != 1024*1024 {
		t.Fatalf("sizes")
	}
}

func TestEBPFCollectorFixture(t *testing.T) {
	c := &ebpfCollector{}
	c.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("1: kprobe  name foo\n\tmemlock 100B  run_time_ns 5  run_cnt 2\n"), nil
	}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := c.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := c.Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("ebpf.programs")
	if !ok {
		t.Fatal("missing chart")
	}
	_, vals := ch.LastValues()
	if vals["loaded"] != 1 {
		t.Fatalf("%v", vals)
	}
	tab, err := c.Functions()[0].Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if tab.(Table).Total != 1 {
		t.Fatalf("%+v", tab)
	}
}
