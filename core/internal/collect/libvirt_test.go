package collect

import (
	"context"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestParseVirshListAndDomstats(t *testing.T) {
	doms := parseVirshList(" Id   Name    State\n-----------------------\n 1    web     running\n -    db      shut off\n")
	if len(doms) != 2 || doms[0].Name != "web" || doms[0].State != "running" || doms[1].State != "shutoff" {
		t.Fatalf("%+v", doms)
	}
	st := parseVirshDomstats("Domain: 'web'\n  state.state=1\n  cpu.time=12345\n  balloon.current=1024\n  balloon.maximum=2048\n  net.0.rx.bytes=10\n  net.0.tx.bytes=20\n  block.0.rd.bytes=30\n  block.0.wr.bytes=40\n")
	if st.CPUTime != 12345 || st.BalloonCur != 1024 || st.RxBytes != 10 || st.WrBytes != 40 || st.State != "running" {
		t.Fatalf("%+v", st)
	}
}

func TestLibvirtCollectorFixture(t *testing.T) {
	c := &libvirtCollector{}
	c.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "list" {
			return []byte(" Id   Name    State\n 1    web     running\n"), nil
		}
		return []byte("Domain: 'web'\n  state.state=1\n  cpu.time=9\n  balloon.current=512\n  balloon.maximum=1024\n  net.0.rx.bytes=1\n  net.0.tx.bytes=2\n  block.0.rd.bytes=3\n  block.0.wr.bytes=4\n"), nil
	}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := c.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := c.Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("libvirt.vm_status.web")
	if !ok {
		t.Fatal("missing status chart")
	}
	_, vals := ch.LastValues()
	if vals["running"] != 1 {
		t.Fatalf("%v", vals)
	}
	if _, ok := reg.Chart("libvirt.vm_cpu.web"); !ok {
		t.Fatal("missing cpu")
	}
	if err := (&libvirtCollector{run: func(context.Context, string, ...string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	}}).Init(reg); err == nil {
		t.Fatal("expected disable")
	}
}
