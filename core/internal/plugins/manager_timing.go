package plugins

import (
	"errors"
	"math"
	"time"
)

// Configured collectors retain their historical nonpositive/default semantics
// and may exceed the manifest's one-day limit. Bound arithmetic before either
// multiplying seconds or converting to a nanosecond Duration, on 32/64-bit Go.
func normalizePluginTiming(sp *Spec, hostEvery int) error {
	maxSeconds := min(int64(math.MaxInt64)/int64(time.Second), int64(^uint(0)>>1))
	if sp.UpdateEvery <= 0 {
		sp.UpdateEvery = max(1, hostEvery)
	}
	if int64(sp.UpdateEvery) > maxSeconds {
		return errors.New("plugin update_every exceeds supported duration")
	}
	if sp.Timeout <= 0 {
		if int64(sp.UpdateEvery) > maxSeconds/10 {
			return errors.New("plugin default timeout exceeds supported duration; set timeout explicitly")
		}
		sp.Timeout = max(10*sp.UpdateEvery, 60)
	}
	if int64(sp.Timeout) > maxSeconds {
		return errors.New("plugin timeout exceeds supported duration")
	}
	return nil
}
