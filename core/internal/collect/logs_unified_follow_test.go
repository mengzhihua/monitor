package collect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestUnifiedFollowRingIsBoundedAndQueriesDoNotDrainCounters(t *testing.T) {
	f := &unifiedFollower{}
	for i := 0; i < 2500; i++ {
		f.append(LogRow{Time: int64(i), Message: fmt.Sprint(i), Unit: "fixture", Priority: "warning"})
	}
	rows, err := f.recent(LogQuery{Limit: 3000})
	if err != nil || len(rows) != 2000 || rows[0].Time != 500 || rows[1999].Time != 2499 {
		t.Fatalf("ring: %d rows, %v", len(rows), err)
	}
	filtered, _ := f.recent(LogQuery{Limit: 3, Query: "249", Priority: "warning"})
	if len(filtered) != 3 || filtered[0].Time != 2497 || filtered[2].Time != 2499 {
		t.Fatalf("filtered: %+v", filtered)
	}
	n, severity, err := f.counters()
	if err != nil || n != 2500 || severity["warning"] != 2500 {
		t.Fatalf("counts: %v %v %v", n, severity, err)
	}
	n, _, _ = f.counters()
	if n != 0 {
		t.Fatal("counters were not drained")
	}
	for i := 0; i < 100; i++ {
		f.append(LogRow{Message: strings.Repeat("x", 64<<10)})
	}
	if f.bytes > unifiedBytes || f.count > 32 {
		t.Fatalf("unbounded bytes=%d rows=%d", f.bytes, f.count)
	}
	// Counters still include rows evicted by the byte budget.
	n, _, _ = f.counters()
	if n != 100 {
		t.Fatalf("large-row counts=%v", n)
	}
}

func TestUnifiedFollowRetryFailureAndStop(t *testing.T) {
	var runs atomic.Int32
	failed := make(chan struct{})
	release := make(chan struct{})
	resumed := make(chan struct{})
	f := newUnifiedFollower(func(ctx context.Context, ready func(), emit func(LogRow)) error {
		if runs.Add(1) == 1 {
			ready()
			emit(LogRow{Message: "partial interval"})
			close(failed)
			return errors.New("test stream failure")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
		}
		ready()
		emit(LogRow{Message: "recovered", Priority: "info"})
		close(resumed)
		<-ctx.Done()
		return ctx.Err()
	}, time.Millisecond)
	defer f.stop()
	<-failed
	deadline := time.Now().Add(time.Second)
	for {
		if _, _, err := f.counters(); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("failed stream still healthy")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := f.recent(LogQuery{}); err == nil {
		t.Fatal("failed stream reported an empty healthy table")
	}
	close(release)
	<-resumed
	n, sev, err := f.counters()
	if err != nil || n != 1 || sev["info"] != 1 {
		t.Fatalf("recovery: %v %v %v", n, sev, err)
	}
	f.stop()
	before := runs.Load()
	time.Sleep(5 * time.Millisecond)
	if runs.Load() != before {
		t.Fatal("stopped stream restarted")
	}
}

func TestUnifiedReadRecordsAndMalformedStream(t *testing.T) {
	line := `{"timestamp":"2024-01-02T03:04:05Z","eventMessage":"fixture","processID":12,"messageType":"Error"}`
	var rows []LogRow
	err := readUnifiedStream(strings.NewReader("Filtering the log data using type == log\n"+line+"\n"), func(r LogRow) { rows = append(rows, r) })
	if err != nil || len(rows) != 1 || rows[0].PID != "12" || rows[0].Priority != "err" || rows[0].Time != 1704164645 {
		t.Fatalf("decode: %+v %v", rows, err)
	}
	if err := readUnifiedStream(strings.NewReader("{bad JSON}\n"), func(LogRow) {}); err == nil {
		t.Fatal("malformed data reported healthy")
	}
	if err := readUnifiedStream(strings.NewReader(strings.Repeat("x", 1<<20)+"\n"), func(LogRow) {}); err == nil {
		t.Fatal("oversized record did not fail")
	}
}

func TestUnifiedFastRecoveryStillLeavesFailedCollectionIntervalMissing(t *testing.T) {
	f := &unifiedFollower{}
	f.append(LogRow{Message: "before failure"})
	f.setError(errors.New("temporary outage"))
	f.setError(nil)
	f.append(LogRow{Message: "after recovery"})
	if rows, err := f.recent(LogQuery{}); err != nil || len(rows) != 2 {
		t.Fatalf("recovered recent rows=%d err=%v", len(rows), err)
	}
	if _, _, err := f.counters(); err == nil {
		t.Fatal("partially collected interval reported healthy")
	}
	f.append(LogRow{Message: "complete interval", Priority: "info"})
	if n, sev, err := f.counters(); err != nil || n != 1 || sev["info"] != 1 {
		t.Fatalf("next interval: %v %v %v", n, sev, err)
	}
}

func TestUnifiedCollectionFailureLeavesAGapAndHistoricalQueriesBypassCache(t *testing.T) {
	f := &unifiedFollower{}
	l := &logsCollector{unified: f}
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	// Register charts without starting any external process.
	disabled := false
	l.cfg.Follow = &disabled
	fixture := filepath.Join(t.TempDir(), "fixture.log")
	if err := os.WriteFile(fixture, nil, 0600); err != nil {
		t.Fatal(err)
	}
	l.cfg.Files = []string{fixture}
	if err := l.Init(reg); err != nil {
		t.Fatal(err)
	}
	f.append(LogRow{Message: "cached", Priority: "err"})
	now := time.Unix(1700000000, 0)
	if err := l.Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	ch, _ := reg.Chart("logs.written")
	ts, values := ch.LastValues()
	if ts != now.Unix() || values["written"] != 1 {
		t.Fatalf("values %d %v", ts, values)
	}
	f.setError(errors.New("unavailable"))
	if err := l.Collect(context.Background(), reg, now.Add(time.Second)); err == nil {
		t.Fatal("failure written as zero")
	}
	next, _ := ch.LastValues()
	if next != ts {
		t.Fatal("failure generated a false sample")
	}
	if _, err := l.Functions()[0].Run(context.Background(), map[string]string{}); err == nil {
		t.Fatal("function suppressed stream error")
	}
	for _, q := range []LogQuery{{After: 1}, {Before: 1}, {Source: "file"}, {Source: "journal"}, {Cursor: "x"}, {Boot: "-1"}} {
		if l.useUnifiedBuffer(q) {
			t.Fatalf("historical/other source query used cache: %+v", q)
		}
	}
}

func TestUnifiedCommandDrainsOutputAndCancels(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var count int
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `i=0; while [ "$i" -lt 200 ]; do printf '%s\n' '{"eventMessage":"fixture"}'; i=$((i+1)); done`)
	if err := runUnifiedStream(ctx, cmd, func() {}, func(LogRow) { count++ }); err != nil || count != 200 {
		t.Fatalf("drain: %d %v", count, err)
	}
	childCtx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runUnifiedStream(childCtx, exec.CommandContext(childCtx, "/bin/sleep", "30"), stop, func(LogRow) {})
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		stop()
		t.Fatal("stream cancellation did not stop child")
	}
}
