package collect

import (
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/preprocess"
)

func TestPreprocessValueRemembersRateBaseline(t *testing.T) {
	store := map[string]preprocess.Memory{}
	steps := []preprocess.Step{{Type: "change_per_second"}}
	start := time.Unix(500, 0)
	if _, ok := preprocessValue(store, "load", "10", steps, start); ok {
		t.Fatal("first rate sample was stored")
	}
	value, ok := preprocessValue(store, "load", "40", steps, start.Add(10*time.Second))
	if !ok || value != 3 {
		t.Fatalf("rate %v ok %v", value, ok)
	}
	if _, ok := preprocessValue(store, "disk", "up", []preprocess.Step{{Type: "valuemap", Map: map[string]string{"up": "1"}}}, start); !ok {
		t.Fatal("valuemap did not produce a sample")
	}
}
