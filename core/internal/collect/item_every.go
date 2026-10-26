package collect

import (
	"fmt"
	"time"
)

func itemEvery(name string, every time.Duration) error {
	if every == 0 {
		return nil
	}
	if every < time.Second || every > 24*time.Hour {
		return fmt.Errorf("%s: every must be 0 or 1s..24h", name)
	}
	return nil
}

// markItemDue reports whether an item should run now. every <= 0 follows the
// global scheduler tick. A positive every keeps at most one run per period.
func markItemDue(last map[string]time.Time, name string, every time.Duration, now time.Time) bool {
	if every <= 0 {
		return true
	}
	if prev, ok := last[name]; ok && now.Sub(prev) < every {
		return false
	}
	last[name] = now
	return true
}
