package collect

import (
	"context"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestIdlejitterCollectorFixture(t *testing.T) {
	i := &idlejitterCollector{cfg: idlejitterConfig{Sleep: 20 * time.Millisecond, Loops: 3}}
	n := 0
	i.sleep = func(d time.Duration) time.Duration {
		n++
		switch n {
		case 1:
			return d + 100*time.Microsecond
		case 2:
			return d + 400*time.Microsecond
		default:
			return d + 300*time.Microsecond
		}
	}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := i.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := i.Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("system.idlejitter")
	if !ok {
		t.Fatal("missing chart")
	}
	_, vals := ch.LastValues()
	if vals["min"] != 100 || vals["max"] != 400 || vals["average"] != 800.0/3 {
		t.Fatalf("%v", vals)
	}
}
