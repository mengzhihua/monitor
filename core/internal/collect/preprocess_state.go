package collect

import (
	"errors"
	"math"
	"time"

	"github.com/mengzhihua/monitor/core/internal/preprocess"
)

// preprocessValue runs the pipeline and remembers a baseline when the step
// only needs it for the next sample. ok is false when this sample should
// not be stored.
func preprocessValue(store map[string]preprocess.Memory, key, input string, steps []preprocess.Step, now time.Time) (float64, bool) {
	prev, ok := store[key]
	if !ok {
		prev.Value = math.NaN()
	}
	value, next, err := preprocess.ApplyAt(steps, input, prev, now)
	if errors.Is(err, preprocess.ErrNoBaseline) || errors.Is(err, preprocess.ErrUnchanged) {
		store[key] = next
		return 0, false
	}
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	store[key] = next
	return value, true
}
