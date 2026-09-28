package collect

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func TestExternalCheckPublishAndExpire(t *testing.T) {
	name := "backup-nightly"
	t.Cleanup(func() { Checks().Remove(name) })
	now := time.Now()
	value := 26.0
	if err := Checks().Report(name, "warning", "last run late", &value, time.Minute, now); err != nil {
		t.Fatal(err)
	}
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{ID: "h", Hostname: "h", UpdateEvery: 1}, db)
	if err := (&checksCollector{}).Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("check." + name)
	if !ok {
		t.Fatal("chart missing")
	}
	if _, last := ch.LastValues(); last["status"] != checkStatusWarning || last["value"] != 26 {
		t.Fatalf("last = %v", last)
	}
	later := now.Add(2 * time.Minute)
	if err := (&checksCollector{}).Collect(context.Background(), reg, later); err != nil {
		t.Fatal(err)
	}
	if _, last := ch.LastValues(); last["status"] != checkStatusExpired {
		t.Fatalf("expired status = %v", last)
	}
	listed := Checks().List(later)
	var found bool
	for _, row := range listed {
		if row.Name == name {
			found = row.Status == "expired" && row.Chart == "check."+name
		}
	}
	if !found {
		t.Fatalf("list = %+v", listed)
	}
	if err := Checks().Report("bad name", "ok", "", nil, 0, now); err == nil {
		t.Fatal("invalid name accepted")
	}
	if err := Checks().Report(name, "down", "", nil, 0, now); err == nil {
		t.Fatal("invalid status accepted")
	}
	nan := math.NaN()
	if err := Checks().Report(name, "ok", "", &nan, 0, now); err == nil {
		t.Fatal("NaN accepted")
	}
	if err := Checks().Report(name, "ok", "cleared", nil, time.Minute, later); err != nil {
		t.Fatal(err)
	}
	if err := (&checksCollector{}).Collect(context.Background(), reg, later); err != nil {
		t.Fatal(err)
	}
	if _, last := ch.LastValues(); last["status"] != checkStatusOK {
		t.Fatalf("cleared status = %v", last)
	}
	for _, row := range Checks().List(later) {
		if row.Name == name && (row.Status != "ok" || row.Expired) {
			t.Fatalf("re-report = %+v", row)
		}
	}
	fns := (&checksCollector{}).Functions()
	if len(fns) != 1 || fns[0].Name != "checks" {
		t.Fatalf("functions = %+v", fns)
	}
	raw, err := fns[0].Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	table, ok := raw.(Table)
	if !ok || table.Total < 1 {
		t.Fatalf("function = %#v", raw)
	}
}
