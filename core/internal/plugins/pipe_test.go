package plugins

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type gatedSink struct {
	once    sync.Once
	started chan struct{}
	release chan struct{}
	rows    atomic.Int64
}

func (s *gatedSink) Append(string, int64, float64) {
	s.once.Do(func() { close(s.started); <-s.release })
	s.rows.Add(1)
}

func TestManagerDrainsOutputAfterProcessExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell and process liveness")
	}
	var script strings.Builder
	script.WriteString("printf '%s' 'CHART drain.v\nDIMENSION v\n")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&script, "BEGIN drain.v\nSET v = %d\nEND\n", i)
	}
	script.WriteString("DISABLE\n'\n")
	path := writeScript(t, t.TempDir(), "drain.plugin", script.String())
	sink := &gatedSink{started: make(chan struct{}), release: make(chan struct{})}
	m := New(newReg(sink), []Spec{{Name: "drain", Command: path}}, Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan struct{})
	var runErr error
	go func() { runErr = m.runOnce(ctx, m.list[0]); close(done) }()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(sink.release) }) }
	defer func() { cancel(); release(); <-done }()
	select {
	case <-sink.started:
	case <-ctx.Done():
		t.Fatal("plugin did not emit a sample")
	}
	// Pause the parser after its first sample. The payload fits in the OS
	// pipe but exceeds the scanner buffer, so process exit cannot mean that
	// all samples or the final DISABLE have reached the parser.
	p := m.list[0]
	p.mu.Lock()
	pid := p.pid
	p.mu.Unlock()
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Release()
	for process.Signal(syscall.Signal(0)) == nil {
		select {
		case <-ctx.Done():
			t.Fatal("plugin did not exit while output was buffered")
		case <-time.After(time.Millisecond):
		}
	}
	release()
	select {
	case <-done:
		if !errors.Is(runErr, ErrDisabled) || sink.rows.Load() != 200 {
			t.Fatalf("final protocol output lost: err=%v samples=%d, want DISABLE and 200", runErr, sink.rows.Load())
		}
	case <-ctx.Done():
		t.Fatal("parser did not drain after process exit")
	}
}
