package tsdb

import (
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"
)

func TestFlushedRollupReleasesEmptyBackingArray(t *testing.T) {
	done := make([]Bucket, 1440)
	for i := range done {
		done[i].TS = int64(i + 1)
	}
	if tail := suffixBucketsNewerThan(done, 1440); tail != nil {
		t.Fatalf("empty flushed buffer still holds its allocation: len=%d cap=%d", len(tail), cap(tail))
	}
}

func TestFlushedTailReleasesHiddenPrefix(t *testing.T) {
	ts, values := make([]int64, 3600), make([]float64, 3600)
	for i := range ts {
		ts[i], values[i] = int64(i+1), float64(i+1)
	}
	tail, vals := suffixNewerThan(ts, values, 3599)
	if len(tail) != 1 || tail[0] != 3600 || len(vals) != 1 || vals[0] != 3600 {
		t.Fatal("tail was lost")
	}
	tail[0], vals[0] = 7, 8
	if ts[3599] == 7 || values[3599] == 8 {
		t.Fatal("one-sample tail still pins its 3,600-sample backing array")
	}
	done := make([]Bucket, 1440)
	for i := range done {
		done[i].TS = int64(i + 1)
	}
	buckets := suffixBucketsNewerThan(done, 1439)
	if len(buckets) != 1 || buckets[0].TS != 1440 {
		t.Fatal("bucket tail was lost")
	}
	buckets[0].TS = 7
	if done[1439].TS == 7 {
		t.Fatal("one-bucket tail still pins its 1,440-bucket backing array")
	}
}

func TestRepeatedRetentionKeepsBackingCapacityVisible(t *testing.T) {
	tier, err := newTier(1, TierSpec{Every: 60, Retention: time.Hour}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sr := tier.getOrCreate("fixture")
	for i := range 1440 {
		tier.append(sr, int64(i+1)*60, 1)
	}
	for i := 1; i < 1439; i++ {
		tier.enforceRetention(time.Unix(int64(i+1)*60+3600, 0))
	}
	if len(sr.done) != 1 || sr.done[0].TS != 1439*60 || sr.open == nil || sr.open.TS != 1440*60 {
		t.Fatalf("retention changed surviving buckets: done=%v open=%v", sr.done, sr.open)
	}
	if cap(sr.done) > 33 {
		t.Fatalf("one bucket retained backing capacity %d", cap(sr.done))
	}
	tier.enforceRetention(time.Unix(1441*60+3600, 0))
	if sr.done != nil || sr.open != nil {
		t.Fatal("expired buffers retain backing storage")
	}
}

func TestSmallerRollupBlocksRecoverLegacyCheckpoint(t *testing.T) {
	options := Options{Dir: t.TempDir(), BlockSize: 100, Tiers: []TierSpec{{Every: 60, BlockSize: 1440}, {Every: 3600, BlockSize: 720}}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s, err := Open(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s != nil {
			_ = s.Close()
		}
	})
	start := time.Now().Unix() - 1301*3600
	start -= start % 3600
	for i := range 1300 {
		s.Append("fixture", start+int64(i)*3600, float64(i%31))
	}
	want := make([][]Bucket, 3)
	for tier := range want {
		want[tier], err = s.QueryTier("fixture", tier, start, start+1300*3600)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Explicit large block settings remain honored on reopen.
	s, err = Open(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.tiers[0].get("fixture").done) != 1299 {
		t.Fatal("explicit block size was overridden")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	options.Tiers = nil
	s, err = Open(options)
	if err != nil {
		t.Fatal(err)
	}
	for _, tier := range s.tiers {
		if sr := tier.get("fixture"); sr.done != nil {
			t.Fatal("legacy checkpoint retained an oversized active rollup")
		}
	}
	for tier := range want {
		got, err := s.QueryTier("fixture", tier, start, start+1300*3600)
		if err != nil || !reflect.DeepEqual(got, want[tier]) {
			t.Fatalf("tier %d changed after migration: %v", tier, err)
		}
	}
	// Reopen once without a new checkpoint: startup archiving must deduplicate
	// against the old image, even when no fresh sample has caused a rewrite.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(options)
	if err != nil {
		t.Fatal(err)
	}
	for tier := range want {
		got, err := s.QueryTier("fixture", tier, start, start+1300*3600)
		if err != nil || !reflect.DeepEqual(got, want[tier]) {
			t.Fatalf("tier %d duplicated migrated buckets: %v", tier, err)
		}
	}
	s.Append("fixture", start+1300*3600, 99)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	for tier := range want {
		got, err := s.QueryTier("fixture", tier, start, start+1300*3600)
		if err != nil || len(got) != len(want[tier])+1 || got[len(got)-1].Last != 99 {
			t.Fatalf("tier %d failed after migration: %v", tier, err)
		}
	}
}
