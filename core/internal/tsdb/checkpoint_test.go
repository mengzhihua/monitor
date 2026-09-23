package tsdb

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckpointFailedWriteKeepsPreviousImage(t *testing.T) {
	s, err := Open(Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Append("x", 1000, 1)
	if err = s.Flush(); err != nil {
		t.Fatal(err)
	}
	s.Append("x", 1001, 2)
	tmp := filepath.Join(s.Dir(), checkpointFile+".tmp")
	if err = os.Mkdir(tmp, 0700); err != nil {
		t.Fatal(err)
	}
	if err = s.Flush(); err == nil {
		t.Fatal("write failure hidden")
	}
	if s.Persistence().Error == "" {
		t.Fatal("failure missing from status")
	}
	recovered, err := Open(Options{Dir: s.Dir()})
	if err != nil {
		t.Fatal(err)
	}
	points, err := recovered.Query("x", 0, 2000)
	// The failed checkpoint must not replace the previous image. The sample
	// taken after that image is still in the WAL, so recovery keeps both.
	if err != nil || len(points) != 2 || points[0].Value != 1 || points[1].Value != 2 {
		t.Fatalf("previous checkpoint lost: %v %v", points, err)
	}
	recovered.Close()
	if err = os.Remove(tmp); err != nil {
		t.Fatal(err)
	}
	if err = s.Flush(); err != nil {
		t.Fatal(err)
	}
	if s.Persistence().Error != "" {
		t.Fatal("error did not clear")
	}
}

func TestCheckpointCorruptionFailsClosed(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	s.Append("x", 1000, 1)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, checkpointFile)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)/2] ^= 0xff
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(Options{Dir: dir}); err == nil {
		t.Fatal("corrupt checkpoint accepted")
	}
}

func TestCheckpointReplaysLaterFullBlocksWithoutDoubleCounting(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, BlockSize: 3, Tiers: []TierSpec{{Every: 60, BlockSize: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	start := int64(1_800_000_000)
	s.Append("existing", start, 1)
	if err = s.Flush(); err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i < 3; i++ {
		s.Append("existing", start+i, float64(i+1))
	}
	for i := int64(0); i < 3; i++ {
		s.Append("new", start+i, 2)
	}
	// Simulate process loss: stop the loop without saving the newer image.
	// Close the WAL handle too. The bytes stay on disk for replay, and Windows
	// cannot delete a file another handle in this process still has open.
	stopWithoutCheckpoint(s)
	recovered, err := Open(Options{Dir: dir, BlockSize: 3, Tiers: []TierSpec{{Every: 60, BlockSize: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	for _, id := range []string{"existing", "new"} {
		pts, err := recovered.Query(id, start, start+10)
		if err != nil || len(pts) != 3 {
			t.Fatalf("%s raw: %v %v", id, pts, err)
		}
		buckets, err := recovered.QueryTier(id, 1, start, start+59)
		if err != nil || len(buckets) != 1 || buckets[0].Count != 3 || buckets[0].Sum != 6 {
			t.Fatalf("%s rollup: %v %v", id, buckets, err)
		}
	}
}

func TestCheckpointRetainsLegacyBlocksAndOpenBuckets(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, Tiers: []TierSpec{{Every: 60, BlockSize: 1440}}})
	if err != nil {
		t.Fatal(err)
	}
	s.Append("legacy", 1200, 10)
	s.Append("legacy", 1201, 20)
	if err = s.flushSeries(s.series["legacy"]); err != nil {
		t.Fatal(err)
	}
	for _, tier := range s.tiers {
		if err = tier.saveOpen(tier.get("legacy")); err != nil {
			t.Fatal(err)
		}
	}
	stopWithoutCheckpoint(s) // old writer had no checkpoint file
	next, err := Open(Options{Dir: dir, Tiers: []TierSpec{{Every: 60, BlockSize: 1440}}})
	if err != nil {
		t.Fatal(err)
	}
	next.Append("legacy", 1202, 30)
	if err = next.Close(); err != nil {
		t.Fatal(err)
	}
	final, err := Open(Options{Dir: dir, Tiers: []TierSpec{{Every: 60, BlockSize: 1440}}})
	if err != nil {
		t.Fatal(err)
	}
	defer final.Close()
	buckets, err := final.QueryTier("legacy", 1, 1200, 1259)
	if err != nil || len(buckets) != 1 || buckets[0].Count != 3 || buckets[0].Sum != 60 {
		t.Fatalf("legacy rollup: %v %v", buckets, err)
	}
	points, err := final.Query("legacy", 1200, 1202)
	if err != nil || len(points) != 3 {
		t.Fatalf("legacy data lost: %v %v", points, err)
	}
}

func stopWithoutCheckpoint(s *Store) {
	close(s.stop)
	s.wg.Wait()
	if s.wal != nil {
		_ = s.wal.close()
	}
}

// A checkpoint Last past every retained sample used to be restored as the
// accept watermark. Collections after a restart were then dropped until wall
// clock caught up, so last_entry stayed on the last visible sample.
func TestCheckpointLastAheadOfSamplesDoesNotFreezeWrites(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	start := int64(1_700_000_000)
	for i := int64(0); i < 5; i++ {
		s.Append("system.load|load1", start+i, float64(i))
	}
	sr := s.series["system.load|load1"]
	sr.mu.Lock()
	sr.last = start + 600
	sr.mu.Unlock()
	if err = s.Flush(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Append("system.load|load1", start+5, 9)
	_, last, ok := s.Bounds("system.load|load1")
	if !ok || last != start+5 {
		t.Fatalf("last_entry frozen at %d ok=%v, want %d", last, ok, start+5)
	}
	pts, err := s.Query("system.load|load1", start, start+5)
	if err != nil || len(pts) != 6 || pts[5].Value != 9 {
		t.Fatalf("sample after restart missing: %v %v", pts, err)
	}
}

// Retention can remove a buffer prefix while a full block is being written.
// Trimming by the snapshot length then drops newer samples or panics, and the
// watermark stays ahead of whatever remains.
func TestFlushKeepsSamplesArrivingDuringWrite(t *testing.T) {
	s, err := Open(Options{Dir: t.TempDir(), BlockSize: 4, Tiers: []TierSpec{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	start := int64(1_700_000_000)
	s.afterSnapshot = func(sr *series) {
		s.afterSnapshot = nil
		sr.mu.Lock()
		sr.ts = sr.ts[1:]
		sr.vals = sr.vals[1:]
		sr.ts = append(sr.ts, start+10)
		sr.vals = append(sr.vals, 99)
		sr.last = start + 10
		sr.mu.Unlock()
	}
	for i := int64(0); i < 4; i++ {
		s.Append("x", start+i, float64(i))
	}
	s.Append("x", start+11, 1)
	_, last, ok := s.Bounds("x")
	if !ok || last != start+11 {
		t.Fatalf("last_entry=%d ok=%v, want %d", last, ok, start+11)
	}
	pts, err := s.Query("x", start+10, start+11)
	if err != nil || len(pts) != 2 || pts[0].Value != 99 || pts[1].Value != 1 {
		t.Fatalf("samples accepted during flush disappeared: %v %v", pts, err)
	}
}

func TestFlushPrefixDropDoesNotPanic(t *testing.T) {
	s, err := Open(Options{Dir: t.TempDir(), BlockSize: 4, Tiers: []TierSpec{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	start := int64(1_700_000_000)
	s.afterSnapshot = func(sr *series) {
		s.afterSnapshot = nil
		sr.mu.Lock()
		sr.ts = sr.ts[:len(sr.ts)-1]
		sr.vals = sr.vals[:len(sr.vals)-1]
		sr.mu.Unlock()
	}
	for i := int64(0); i < 4; i++ {
		s.Append("x", start+i, float64(i))
	}
	s.Append("x", start+4, 4)
	_, last, ok := s.Bounds("x")
	if !ok || last != start+4 {
		t.Fatalf("last_entry=%d ok=%v, want %d", last, ok, start+4)
	}
}

func TestTierFlushKeepsBucketsAppendedDuringWrite(t *testing.T) {
	s, err := Open(Options{Dir: t.TempDir(), Tiers: []TierSpec{{Every: 60, BlockSize: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tier := s.tiers[0]
	tier.afterSnapshot = func(sr *tierSeries) {
		tier.afterSnapshot = nil
		sr.mu.Lock()
		sr.done = sr.done[1:]
		sr.done = append(sr.done, Bucket{TS: 300, Min: 7, Max: 7, Sum: 7, Last: 7, Count: 1})
		sr.mu.Unlock()
	}
	// ts 0 is not newer than the zero watermark, so the first real bucket is 60.
	for _, ts := range []int64{60, 120, 180} {
		s.Append("x", ts, 1)
	}
	buckets, err := s.QueryTier("x", 1, 0, 360)
	if err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, b := range buckets {
		if b.TS == 300 && b.Count == 1 {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("bucket appended during tier flush disappeared: %+v", buckets)
	}
}

func TestCheckpointRawRetentionIncludesMemory(t *testing.T) {
	s, err := Open(Options{Dir: t.TempDir(), Retention: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Append("x", time.Now().Add(-time.Hour).Unix(), 1)
	s.Append("x", time.Now().Unix(), 2)
	s.enforceRetention()
	if len(s.series["x"].ts) != 1 {
		t.Fatal("expired memory retained")
	}
	points, err := s.Query("x", 0, time.Now().Unix()+1)
	if err != nil || len(points) != 1 || points[0].Value != 2 {
		t.Fatalf("retention: %v %v", points, err)
	}
}
