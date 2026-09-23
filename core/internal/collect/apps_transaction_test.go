package collect

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestAppsAbortedPassPreservesPublishedCountersAndRecovery(t *testing.T) {
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	a := &appsCollector{}
	if err := a.Init(reg); err != nil {
		t.Fatal(err)
	}
	a.hasIO = true
	st := &pidState{name: "fixture", group: a.other, user: "fixture_user", osGroup: "fixture_group"}
	a.pids = map[int32]*pidState{42: st}
	now := time.Unix(1700000000, 0)
	stage := func(cpu float64, read, write uint64) {
		a.samples = []appProcessUpdate{{pid: 42, state: st, counters: procCounters{
			cpuSec: cpu, rss: 1024, threads: 2, readB: read, writeB: write, ok: true, hasIO: true,
		}}}
	}
	stage(1, 100, 200)
	if err := a.finishSamples(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	before := *st
	beforeTable := a.processes(nil)
	if beforeTable.CollectedAt != now.Unix() {
		t.Fatal("table does not expose its actual collection timestamp")
	}
	ch, _ := reg.Chart("apps.mem")
	beforeTime, beforeValues := ch.LastValues()
	stage(1.1, 150, 260)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.finishSamples(ctx, reg, now.Add(time.Second)); !errors.Is(err, context.Canceled) {
		t.Fatalf("aborted pass: %v", err)
	}
	if !reflect.DeepEqual(before, *st) || a.last != now || !reflect.DeepEqual(beforeTable, a.processes(nil)) {
		t.Fatal("aborted pass changed the baseline or published table")
	}
	afterTime, afterValues := ch.LastValues()
	if beforeTime != afterTime || !reflect.DeepEqual(beforeValues, afterValues) ||
		a.cpuMs[a.other.name] != 0 || a.cpuUser[st.user] != 0 || a.cpuOSGroup[st.osGroup] != 0 ||
		a.readB[a.other.name] != 0 || a.writeB[a.other.name] != 0 {
		t.Fatal("aborted pass changed a chart or cumulative counter")
	}
	stage(1.2, 180, 300)
	if err := a.finishSamples(context.Background(), reg, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if a.processes(nil).CollectedAt != now.Add(2*time.Second).Unix() {
		t.Fatal("recovery did not advance the published timestamp")
	}
	if math.Abs(st.cpuPct-10) > 1e-9 || math.Abs(a.cpuMs[a.other.name]-200) > 1e-9 ||
		math.Abs(a.cpuUser[st.user]-200) > 1e-9 || math.Abs(a.cpuOSGroup[st.osGroup]-200) > 1e-9 ||
		a.readB[a.other.name] != 80 || a.writeB[a.other.name] != 100 {
		t.Fatal("recovery lost, duplicated or overestimated CPU/IO across the aborted interval")
	}
	// A single unreadable PID must use its own last valid sample time on recovery.
	a.samples[0].counters = procCounters{}
	if err := a.finishSamples(context.Background(), reg, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if st.cpuPct != 0 || st.cpuAt != now.Add(2*time.Second) {
		t.Fatal("unreadable PID advanced its valid CPU sample time")
	}
	stage(1.4, 200, 320)
	if err := a.finishSamples(context.Background(), reg, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if math.Abs(st.cpuPct-10) > 1e-9 || math.Abs(a.cpuUser[st.user]-400) > 1e-9 ||
		math.Abs(a.cpuOSGroup[st.osGroup]-400) > 1e-9 || math.Abs(a.cpuMs[a.other.name]-400) > 1e-9 {
		t.Fatal("per-PID read gap changed the rate denominator or owner totals")
	}
	// Every caller gets an independent table; sorting/filtering cannot edit
	// the published backing slice used by a concurrent request.
	table := a.processes(nil)
	row := table.Rows[0].(ProcessRow)
	row.Name = "caller edit"
	table.Rows[0] = row
	if a.processes(nil).Rows[0].(ProcessRow).Name != "fixture" {
		t.Fatal("caller modified the published table")
	}
}
