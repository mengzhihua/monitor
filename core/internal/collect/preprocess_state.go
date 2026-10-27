package collect

import (
	"errors"
	"math"
	"time"

	"github.com/mengzhihua/monitor/core/internal/preprocess"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

// preprocessValue runs the pipeline with collection time and per-item memory.
func preprocessValue(store map[string]preprocess.Memory, key, input string, steps []preprocess.Step, now time.Time, reg *registry.Registry, chartID string) (float64, bool) {
	prev, ok := store[key]
	if !ok {
		prev.Value = math.NaN()
	}
	value, next, err := preprocess.ApplyAt(steps, input, prev, now)
	if errors.Is(err, preprocess.ErrNoBaseline) || errors.Is(err, preprocess.ErrUnchanged) || errors.Is(err, preprocess.ErrThrottled) {
		store[key] = next
		return 0, false
	}
	if noteUnsupported(reg, chartID, err) {
		store[key] = next
		return 0, false
	}
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	store[key] = next
	return value, true
}
