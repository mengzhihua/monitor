package plugins

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// Pause the initial cancellation check after it observes a live request. This
// models cancellation between acquiring the control gate and starting discovery.
type discoveryGateContext struct {
	context.Context
	once    sync.Once
	entered chan struct{}
	resume  chan struct{}
}

func (ctx *discoveryGateContext) Err() error {
	err := ctx.Context.Err()
	ctx.once.Do(func() {
		close(ctx.entered)
		<-ctx.resume
	})
	return err
}

func TestManagerRescanCanceledAfterAcquiringGate(t *testing.T) {
	m := New(newReg(&memSink{}), nil, Options{Dir: t.TempDir()})
	stop := runTestManager(t, m)
	before := m.Catalog()
	request, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &discoveryGateContext{Context: request, entered: make(chan struct{}), resume: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, err := m.Apply(ctx, ControlRequest{Action: "rescan", IfRevision: before.Revision})
		done <- err
	}()
	<-ctx.entered
	cancel()
	close(ctx.resume)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("canceled rescan returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled rescan kept the control gate")
	}
	if after := m.Catalog(); !reflect.DeepEqual(after, before) {
		t.Fatalf("canceled rescan published a catalog: before=%+v after=%+v", before, after)
	}
	stop()
}

type cancelDiscoveryRead struct {
	cancel context.CancelFunc
	reads  int
}

func (r *cancelDiscoveryRead) Read(p []byte) (int, error) {
	r.reads++
	n := copy(p, "one block")
	r.cancel()
	return n, nil
}

func (*cancelDiscoveryRead) WriteTo(io.Writer) (int64, error) {
	panic("io.Copy bypassed the cancellable reader")
}

func TestDiscoveryHashStopsBetweenReads(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &cancelDiscoveryRead{cancel: cancel}
	// Use the actual fingerprint destination: a hash that cannot bypass Read
	// with an optimized ReaderFrom. Cancellation during the first read must
	// stop the next block, even if the source offers WriterTo.
	n, err := io.Copy(sha256.New(), discoveryReader{ctx: ctx, reader: source})
	if !errors.Is(err, context.Canceled) || source.reads != 1 || n != int64(len("one block")) {
		t.Fatalf("hash kept reading after cancellation: reads=%d bytes=%d err=%v", source.reads, n, err)
	}
}

func TestCanceledDiscoveryDiscardsCandidates(t *testing.T) {
	dir := t.TempDir()
	path := packageFixture(t, dir, "example", nil)
	if err := os.WriteFile(filepath.Join(dir, "legacy.plugin"), []byte("content"), 0o700); err != nil {
		t.Fatal(err)
	}
	m := New(newReg(&memSink{}), nil, Options{Dir: dir})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if list, diagnostics, err := m.discoverCatalog(ctx); !errors.Is(err, context.Canceled) || list != nil || diagnostics != nil {
		t.Fatalf("canceled catalog returned candidates: plugins=%d diagnostics=%v err=%v", len(list), diagnostics, err)
	}
	if specs, diagnostics, err := discoverManifests(ctx, dir); !errors.Is(err, context.Canceled) || specs != nil || diagnostics != nil {
		t.Fatalf("canceled packages returned candidates: specs=%d diagnostics=%v err=%v", len(specs), diagnostics, err)
	}
	if specs, err := discover(ctx, dir); !errors.Is(err, context.Canceled) || specs != nil {
		t.Fatalf("canceled legacy discovery returned candidates: specs=%d err=%v", len(specs), err)
	}
	if fingerprint, err := specFingerprint(ctx, Spec{Command: path}); !errors.Is(err, context.Canceled) || fingerprint != "" {
		t.Fatalf("canceled hashing returned a fingerprint: %q %v", fingerprint, err)
	}
}
