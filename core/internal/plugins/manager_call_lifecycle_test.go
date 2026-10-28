package plugins

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFunctionNativePipeCancellationStopsOwnedProcess(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	processCtx, cancelProcess := context.WithCancel(context.Background())
	defer cancelProcess()
	observed := &observedCallInput{WriteCloser: writer, entered: make(chan struct{})}
	input := &functionInput{WriteCloser: observed, cancel: cancelProcess}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelDone := make(chan struct{})
	go func() {
		defer close(cancelDone)
		select {
		case <-observed.entered:
			// Cancellation starts after Write is entered, not while a test
			// goroutine waits to be scheduled on a loaded machine.
			time.Sleep(50 * time.Millisecond)
			cancel()
		case <-ctx.Done():
		}
	}()
	// More than an anonymous pipe can buffer: the reader deliberately does not
	// consume. This exercises the same OS pipe used by cmd.StdinPipe.
	var rescued atomic.Bool
	rescue := time.AfterFunc(2*time.Second, func() {
		rescued.Store(true)
		_ = reader.Close()
	})
	defer rescue.Stop()
	err = writeFunctionCall(ctx, input, make([]byte, 1<<20))
	cancel()
	<-cancelDone
	if rescued.Load() {
		t.Fatal("native pipe write required emergency reader closure after cancellation")
	}
	if !errors.Is(err, context.Canceled) || processCtx.Err() == nil {
		t.Fatalf("write=%v process=%v", err, processCtx.Err())
	}
}

type observedCallInput struct {
	io.WriteCloser
	entered chan struct{}
	once    sync.Once
	closed  atomic.Bool
}

func (input *observedCallInput) Write(data []byte) (int, error) {
	input.once.Do(func() { close(input.entered) })
	return input.WriteCloser.Write(data)
}
func (input *observedCallInput) Close() error {
	input.closed.Store(true)
	return input.WriteCloser.Close()
}
func callFixture(t *testing.T) (*Manager, *plugin, *io.PipeReader, *observedCallInput, FunctionSpec) {
	t.Helper()
	reader, writer := io.Pipe()
	input := &observedCallInput{WriteCloser: writer, entered: make(chan struct{})}
	t.Cleanup(func() { _ = reader.Close(); _ = input.Close() })
	parser := &Parser{Plugin: "fixture", Namespace: "plugin.fixture.", AllowsFunctions: true}
	if err := parser.Line("FUNCTION ping 1 ping"); err != nil {
		t.Fatal(err)
	}
	p := &plugin{spec: Spec{Name: "fixture"}, parser: parser, state: StateRunning, enabled: true, stdin: input, call: make(chan struct{}, 1)}
	m := &Manager{list: []*plugin{p}, running: true}
	functions := m.Functions()
	if len(functions) != 1 {
		t.Fatalf("functions=%v", functions)
	}
	return m, p, reader, input, functions[0]
}
func TestFunctionBlockedInputHonorsCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[deadline], func(t *testing.T) {
			_, _, _, input, function := callFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			var cancelDone chan struct{}
			want := context.Canceled
			if deadline {
				cancel()
				want = context.DeadlineExceeded
				ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
			} else {
				cancelDone = make(chan struct{})
				go func() {
					defer close(cancelDone)
					select {
					case <-input.entered:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			defer cancel()
			// Call synchronously so the request deadline does not run while a
			// test goroutine waits for its first scheduling turn. The rescue
			// closes the pipe if cancellation regresses, keeping failure bounded.
			var rescued atomic.Bool
			rescue := time.AfterFunc(2*time.Second, func() {
				rescued.Store(true)
				_ = input.Close()
			})
			defer rescue.Stop()
			_, err := function.Run(ctx, nil)
			cancel()
			if cancelDone != nil {
				<-cancelDone
			}
			select {
			case <-input.entered:
			default:
				t.Fatalf("CALL write never started before context expired: %v", err)
			}
			if rescued.Load() {
				t.Fatal("blocked stdin write required emergency cleanup after cancellation/deadline")
			}
			if !errors.Is(err, want) {
				t.Fatalf("call=%v, want %v", err, want)
			}
		})
	}
}
func TestFunctionResultTimeoutDoesNotCloseDeliveredInput(t *testing.T) {
	_, _, reader, input, function := callFixture(t)
	delivered := make(chan struct{})
	readerReady := make(chan struct{})
	go func() {
		close(readerReady)
		_, _ = bufio.NewReader(reader).ReadString('\n')
		close(delivered)
	}()
	<-readerReady
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := function.Run(ctx, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("call=%v", err)
	}
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("function timed out before delivering CALL")
	}
	if input.closed.Load() {
		t.Fatal("waiting for a result closed an already delivered input")
	}
}
func TestFunctionProcessNaturalExitReleasesInput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell process; blocked-input cancellation is tested on every platform")
	}
	path := writeScript(t, t.TempDir(), "function.plugin", "printf 'FUNCTION ping 1 ping\\n'\nIFS= read -r quit\nexit 0\n")
	m := New(newReg(&memSink{}), []Spec{{Name: "fixture", Command: path, Timeout: 30,
		Manifest: &Manifest{ID: "fixture", Capabilities: []string{"functions"}}}}, Options{})
	p := m.list[0]
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var runErr error
	go func() { runErr = m.runOnce(ctx, p); close(done) }()
	defer func() {
		cancel()
		p.mu.Lock()
		input := p.stdin
		p.mu.Unlock()
		if input != nil {
			_ = input.Close()
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("function process cleanup did not join")
		}
	}()
	// Wait for the actual declaration before measuring process teardown. A
	// loaded host can delay executable startup; that is not an input-drain bug.
	waitFor(t, func() bool {
		p.mu.Lock()
		parser := p.parser
		p.mu.Unlock()
		return len(parser.Functions()) == 1
	}, "function process ready to exit")
	p.mu.Lock()
	input := p.stdin
	p.mu.Unlock()
	ctxWrite, stopWrite := context.WithTimeout(ctx, time.Second)
	defer stopWrite()
	if err := writeFunctionCall(ctxWrite, input, []byte("quit\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		if runErr != nil {
			t.Fatal(runErr)
		}
	case <-time.After(5 * time.Second):
		stacks := make([]byte, 1<<20)
		t.Logf("function exit stacks:\n%s", stacks[:runtime.Stack(stacks, true)])
		p.mu.Lock()
		input := p.stdin
		p.mu.Unlock()
		if input != nil {
			_ = input.Close()
		}
		cancel()
		<-done
		t.Fatal("exited function process retained its stdin copy goroutine")
	}
}

func TestFunctionDirectoryTracksCurrentRunningGeneration(t *testing.T) {
	m, p, _, input, stale := callFixture(t)
	for _, state := range []State{StateStarting, StateWaiting, StateStopped, StateDisabled, StateFailed} {
		p.state = state
		if got := m.Functions(); len(got) != 0 {
			t.Fatalf("state=%s functions=%v", state, got)
		}
	}
	p.state = StateRunning
	p.enabled = false
	if len(m.Functions()) != 0 {
		t.Fatal("disabled plugin exposes functions")
	}
	p.enabled, p.stdin = true, nil
	if len(m.Functions()) != 0 {
		t.Fatal("missing input exposes functions")
	}
	p.stdin = input
	m.running = false
	if len(m.Functions()) != 0 {
		t.Fatal("stopped manager exposes functions")
	}
	m.running = true
	p.parser = &Parser{Plugin: "fixture", Namespace: "plugin.fixture.", AllowsFunctions: true}
	if len(m.Functions()) != 0 {
		t.Fatal("new generation inherited old declarations")
	}
	if err := p.parser.Line("FUNCTION pong 2 pong"); err != nil {
		t.Fatal(err)
	}
	if got := m.Functions(); len(got) != 1 || got[0].Name != "plugin.fixture.pong" {
		t.Fatalf("functions=%v", got)
	}
	if _, err := stale.Run(context.Background(), nil); err == nil {
		t.Fatal("stale function reached replacement parser")
	}
	select {
	case <-input.entered:
		t.Fatal("stale function wrote to replacement process")
	default:
	}
	current := m.Functions()[0]
	m.list = []*plugin{{spec: p.spec, parser: p.parser, stdin: input, state: StateRunning, enabled: true, call: make(chan struct{}, 1)}}
	if _, err := current.Run(context.Background(), nil); err == nil {
		t.Fatal("stale function reached replacement plugin")
	}
	select {
	case <-input.entered:
		t.Fatal("stale function wrote to replacement plugin")
	default:
	}
}

func TestFunctionProcessExitRestartsAndManagerShutdownJoins(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell process; native pipe cancellation runs on every platform")
	}
	path := writeScript(t, t.TempDir(), "function.plugin", "printf 'FUNCTION ping 1 ping\\n'\nexit 0\n")
	m := New(newReg(&memSink{}), []Spec{{Name: "fixture", Command: path, Timeout: 30,
		Manifest: &Manifest{ID: "fixture", Capabilities: []string{"functions"}}}}, Options{RestartMin: 20 * time.Millisecond, RestartMax: 20 * time.Millisecond})
	stop := runTestManager(t, m)
	waitFor(t, func() bool { return statusNamed(m, "fixture").Restarts >= 2 }, "natural function exits to restart")
	stop()
	if m.Catalog().Running || len(m.Functions()) != 0 {
		t.Fatal("stopped manager retains callable functions")
	}
}
