package collect

import (
	"context"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestParseIoping(t *testing.T) {
	st := parseIoping("1 requests completed in 1.0 ms, 4 kB, 1.00 iops, 4.0 kB/s\nmin/avg/max/mdev = 0.1 ms / 0.2 ms / 0.5 ms / 0.05 ms\n")
	if st.IOPS != 1 || st.Min != 100 || st.Avg != 200 || st.Max != 500 {
		t.Fatalf("%+v", st)
	}
}

func TestIopingCollectorFixture(t *testing.T) {
	i := &iopingCollector{}
	i.cfg.Device = "/dev/sda"
	i.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("1 requests, 1.00 iops\nmin/avg/max/mdev = 100 us / 200 us / 300 us / 10 us\n"), nil
	}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := i.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := i.Collect(context.Background(), reg, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("ioping.latency")
	if !ok {
		t.Fatal("missing latency")
	}
	_, vals := ch.LastValues()
	if vals["avg"] != 200 {
		t.Fatalf("%v", vals)
	}
	if err := (&iopingCollector{}).Init(reg); err == nil {
		t.Fatal("expected disable without device")
	}
}
