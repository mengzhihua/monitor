package registry

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSubscriptionCancelIsIdempotentAndSelfSafe(t *testing.T) {
	r := newReg(nil)
	r.AddChart(&Chart{ID: "x", Dimensions: []*Dimension{{ID: "a"}}})
	var calls, laterCalls int
	var cancelSelf, cancelLater func()
	cancelSelf = r.SubscribeCancelable(func(string, int64, map[string]float64) {
		calls++
		cancelSelf()
		// Both callbacks were in this notification snapshot. Cancellation
		// prevents the later callback from starting despite that old snapshot.
		cancelLater()
		r.Subscribe(func(string, int64, map[string]float64) { laterCalls++ })
	})
	cancelLater = r.SubscribeCancelable(func(string, int64, map[string]float64) {
		t.Error("callback canceled before invocation still ran")
	})
	if err := r.Collect("x", time.Unix(1, 0), map[string]float64{"a": 3}); err != nil {
		t.Fatal(err)
	}
	cancelSelf()
	cancelLater()
	if calls != 1 || laterCalls != 0 || len(r.subs) != 1 {
		t.Fatalf("first notification: calls=%d later=%d subscribers=%d", calls, laterCalls, len(r.subs))
	}
	if err := r.Ingest("x", 2, map[string]float64{"a": 4}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || laterCalls != 1 {
		t.Fatalf("next notification: calls=%d later=%d", calls, laterCalls)
	}
}

func TestSubscriptionCancelDoesNotWaitForInFlightCallback(t *testing.T) {
	r := newReg(nil)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	cancel := r.SubscribeCancelable(func(string, int64, map[string]float64) {
		calls.Add(1)
		close(entered)
		<-release
	})
	go func() {
		defer close(finished)
		r.notifySubscribers("x", 1, nil)
	}()
	<-entered
	canceled := make(chan struct{})
	go func() { cancel(); close(canceled) }()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("cancellation waited for an in-flight callback")
	}
	r.notifySubscribers("x", 2, nil)
	close(release)
	<-finished
	if calls.Load() != 1 || len(r.subs) != 0 {
		t.Fatalf("calls=%d retained subscribers=%d", calls.Load(), len(r.subs))
	}
}

func TestSubscriptionConcurrentCancelAndNotify(t *testing.T) {
	r := newReg(nil)
	var calls atomic.Int64
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for i := range 200 {
				cancel := r.SubscribeCancelable(func(string, int64, map[string]float64) { calls.Add(1) })
				r.notifySubscribers("x", int64(i), nil)
				var cancelers sync.WaitGroup
				cancelers.Go(cancel)
				cancelers.Go(cancel)
				cancelers.Wait()
			}
		})
	}
	workers.Wait()
	if len(r.subs) != 0 || calls.Load() == 0 {
		t.Fatalf("calls=%d retained subscribers=%d", calls.Load(), len(r.subs))
	}
	if allocations := testing.AllocsPerRun(100, func() { r.notifySubscribers("x", 0, nil) }); allocations != 0 {
		t.Fatalf("empty notification allocated %g objects", allocations)
	}
}

func BenchmarkSubscriptionNotify(b *testing.B) {
	for _, active := range []bool{false, true} {
		name := "empty"
		if active {
			name = "subscribed"
		}
		b.Run(name, func(b *testing.B) {
			r := newReg(nil)
			if active {
				defer r.SubscribeCancelable(func(string, int64, map[string]float64) {})()
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				r.notifySubscribers("x", 0, nil)
			}
		})
	}
}
