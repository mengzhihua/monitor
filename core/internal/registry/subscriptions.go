package registry

import "sync/atomic"

type subscription struct {
	fn       func(chartID string, ts int64, values map[string]float64)
	canceled atomic.Bool
}

// Subscribe registers a callback for every completed chart collection with its
// final (post-algorithm) values. The subscription lasts as long as the registry.
// Owners with a shorter lifetime should use SubscribeCancelable instead.
func (r *Registry) Subscribe(fn func(chartID string, ts int64, values map[string]float64)) {
	r.SubscribeCancelable(fn)
}

// SubscribeCancelable returns an idempotent cancellation function. Cancellation
// removes the subscription from future notification snapshots without waiting
// for callbacks already in flight. A callback that passed its cancellation check
// may finish after cancel returns, so owners must guard their own closed state.
// Callbacks run outside registry locks and may cancel themselves or subscribe.
func (r *Registry) SubscribeCancelable(fn func(chartID string, ts int64, values map[string]float64)) func() {
	sub := &subscription{fn: fn}
	r.mu.Lock()
	// Notifications hold this snapshot after releasing the lock. Never mutate
	// its backing array, including when removing or appending a subscription.
	next := make([]*subscription, len(r.subs)+1)
	copy(next, r.subs)
	next[len(r.subs)] = sub
	r.subs = next
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if sub.canceled.Swap(true) {
			return
		}
		for i, candidate := range r.subs {
			if candidate != sub {
				continue
			}
			var next []*subscription
			if len(r.subs) > 1 {
				next = make([]*subscription, len(r.subs)-1)
				copy(next, r.subs[:i])
				copy(next[i:], r.subs[i+1:])
			}
			r.subs = next
			return
		}
	}
}

func (r *Registry) notifySubscribers(chartID string, ts int64, values map[string]float64) {
	r.mu.RLock()
	subs := r.subs
	r.mu.RUnlock()
	for _, sub := range subs {
		if !sub.canceled.Load() {
			sub.fn(chartID, ts, values)
		}
	}
}
