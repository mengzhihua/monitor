package registry

import (
	"testing"
	"time"
)

func TestLatestValuesExcludeMissingDimensions(t *testing.T) {
	r := newReg(nil)
	c := r.AddChart(&Chart{ID: "memory", Dimensions: []*Dimension{{ID: "used"}, {ID: "free"}}})
	_ = r.Collect("memory", time.Unix(100, 0), map[string]float64{"used": 75, "free": 25})
	_ = r.Collect("memory", time.Unix(101, 0), map[string]float64{"free": 30})
	at, values := c.LatestValues()
	if at != 101 || len(values) != 1 || values["free"] != 30 {
		t.Fatalf("mixed generations: %d %v", at, values)
	}
	_, cached := c.LastValues()
	if cached["used"] != 75 {
		t.Fatal("legacy last-value semantics changed")
	}
	c = r.ReplaceChart(&Chart{ID: "memory", Dimensions: []*Dimension{{ID: "used"}, {ID: "free"}}})
	_, values = c.LatestValues()
	if len(values) != 1 {
		t.Fatal("definition update revived stale dimensions")
	}
	_ = r.Ingest("memory", 102, map[string]float64{"used": 80, "free": 20})
	_ = r.Ingest("memory", 90, map[string]float64{"used": 1, "free": 99})
	at, values = c.LatestValues()
	if at != 102 || values["used"] != 80 {
		t.Fatal("history replay replaced current sample")
	}
}
