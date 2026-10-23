package health

import (
	"sync"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

type blockedLookupAnomaly struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockedLookupAnomaly) Rate(string, string) (float64, bool) { return 25, true }

func (s *blockedLookupAnomaly) RatesBetween(string, string, int64, int64) []float64 {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return []float64{25}
}

// Replacing the anomaly provider must remain possible during a slow lookup,
// and every dimension in that lookup must still use its original provider.
func TestLookupProviderReplacementDuringRead(t *testing.T) {
	e, chart := lookupFixture(t, 2)
	source := &blockedLookupAnomaly{entered: make(chan struct{}), release: make(chan struct{})}
	e.SetAnomaly(source)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(source.release) }) }
	defer release()
	lookup := &Lookup{Method: tsdb.GroupAverage, After: time.Minute, AnomalyBit: true}
	now := time.Unix(1_700_000_000, 0)
	result := make(chan float64, 1)
	go func() { result <- e.lookup(chart, lookup, now) }()
	select {
	case <-source.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("lookup did not reach the original provider")
	}

	replaced := make(chan struct{})
	go func() {
		e.SetAnomaly(testAnom{vals: []float64{100}})
		close(replaced)
	}()
	select {
	case <-replaced:
	case <-time.After(5 * time.Second):
		t.Fatal("provider replacement blocked behind an external lookup")
	}
	release()
	select {
	case value := <-result:
		if value != 50 {
			t.Fatalf("in-flight lookup mixed providers: got %v, want 50", value)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lookup did not finish after the provider resumed")
	}
	if value := e.lookup(chart, lookup, now); value != 200 {
		t.Fatalf("next lookup did not use the replacement provider: got %v, want 200", value)
	}
}
