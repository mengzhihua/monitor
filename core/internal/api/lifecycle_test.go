package api

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func newLifecycleTestServer(t *testing.T, opt Options, onSample func(string, int64, map[string]float64)) *Server {
	t.Helper()
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{ID: "lifecycle", Hostname: "lifecycle", UpdateEvery: 1}, db)
	if onSample != nil {
		reg.Subscribe(onSample)
	}
	sched := collect.NewScheduler(reg, nil, collect.Options{Names: []string{"none"}})
	s, err := New(reg, db, sched, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func waitForReport(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		files, err := filepath.Glob(filepath.Join(dir, "availability-*.csv"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("background report was not written")
}

func TestServerCloseJoinsBackgroundWorkers(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	dir := t.TempDir()
	s := newLifecycleTestServer(t, Options{SelfMonitor: true, ReportEvery: time.Hour, ReportDir: dir}, func(id string, _ int64, _ map[string]float64) {
		if id == "monitor.notify_queue" {
			enteredOnce.Do(func() { close(entered) })
			<-release
		}
	})
	// Release before the server cleanup even when an assertion fails.
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("self monitor did not start")
	}
	waitForReport(t, dir)

	const callers = 8
	closed := make(chan struct{}, callers)
	for range callers {
		go func() {
			s.Close()
			closed <- struct{}{}
		}()
	}
	select {
	case <-closed:
		t.Fatal("Close returned while a background collection was still running")
	case <-time.After(20 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	for range callers {
		select {
		case <-closed:
		case <-time.After(2 * time.Second):
			t.Fatal("Close did not cancel workers waiting on their next tick")
		}
	}
	s.Close()
	s.shell.mu.Lock()
	shellClosed := s.shell.closed
	s.shell.mu.Unlock()
	if !shellClosed {
		t.Fatal("Close did not shut down shell sessions")
	}
}

func TestServerCloseStopsReportWrites(t *testing.T) {
	dir := t.TempDir()
	s := newLifecycleTestServer(t, Options{ReportEvery: 5 * time.Millisecond, ReportDir: dir}, nil)
	waitForReport(t, dir)
	s.Close()
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if err := os.Remove(filepath.Join(dir, file.Name())); err != nil {
			t.Fatal(err)
		}
	}
	// Several report intervals must pass without the canceled worker writing again.
	time.Sleep(30 * time.Millisecond)
	files, err = os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("report worker wrote after Close: files=%v err=%v", files, err)
	}
}
