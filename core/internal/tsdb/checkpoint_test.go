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
	close(s.stop)
	s.wg.Wait()
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
	close(s.stop)
	s.wg.Wait() // old writer had no checkpoint file
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
