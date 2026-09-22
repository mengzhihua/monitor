package collect

import (
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func TestCorrelateGroupedDimension(t *testing.T) {
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{ID: "h", Hostname: "h", UpdateEvery: 1}, db)
	reg.AddChart(&registry.Chart{ID: "net.eth0", Context: "net.net", Title: "eth0", Units: "kb/s",
		Dimensions: []*registry.Dimension{{ID: "received"}, {ID: "sent"}}})
	reg.AddChart(&registry.Chart{ID: "system.uptime", Context: "system.uptime", Title: "uptime", Units: "seconds",
		Dimensions: []*registry.Dimension{{ID: "uptime"}}})
	now := time.Now().Truncate(time.Second)
	for i := 0; i < 40; i++ {
		recv := 10.0
		if i >= 30 {
			recv = 80
		}
		_ = reg.Collect("net.eth0", now.Add(time.Duration(i-39)*time.Second), map[string]float64{"received": recv, "sent": 2})
		_ = reg.Collect("system.uptime", now.Add(time.Duration(i-39)*time.Second), map[string]float64{"uptime": float64(100 + i)})
	}
	after, before := now.Unix()-10, now.Unix()
	baseAfter, baseBefore := now.Unix()-40, now.Unix()-10
	got := CorrelateGrouped(reg, db, "volume", after, before, baseAfter, baseBefore, "dimension", 10)
	if len(got) == 0 {
		t.Fatal("expected dimension weights")
	}
	if got[0].Dimension == "" || got[0].Chart != "net.eth0" {
		t.Fatalf("top = %+v", got[0])
	}
	ctx := CorrelateGrouped(reg, db, "volume", after, before, baseAfter, baseBefore, "context", 10)
	if len(ctx) == 0 || ctx[0].Context != "net.net" {
		t.Fatalf("context group = %+v", ctx)
	}
}
