package tsdb

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// Exit without Close after a successful checkpoint, just as a killed process
// would. Both raw history and an unfinished minute bucket must survive.
func TestCheckpointCrashRecovery(t *testing.T) {
	start := time.Now().Unix()/60*60 - 120
	if val := os.Getenv("MONITOR_CRASH_TEST_START"); val != "" {
		start, _ = strconv.ParseInt(val, 10, 64)
	}
	if dir := os.Getenv("MONITOR_CRASH_TEST_DIR"); dir != "" {
		s, err := Open(Options{Dir: dir})
		if err != nil {
			os.Exit(2)
		}
		s.Append("test", start, 10)
		s.Append("test", start+1, 20)
		if s.Flush() != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCheckpointCrashRecovery$")
	cmd.Env = append(os.Environ(), "MONITOR_CRASH_TEST_DIR="+dir, "MONITOR_CRASH_TEST_START="+strconv.FormatInt(start, 10))
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, b)
	}
	s, err := Open(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Append("test", start+2, 30)
	pts, err := s.Query("test", start, start+2)
	if err != nil || len(pts) != 3 {
		t.Fatalf("raw %v %v", pts, err)
	}
	b, err := s.QueryTier("test", 1, start, start+59)
	if err != nil || len(b) != 1 || b[0].Count != 3 || b[0].Sum != 60 {
		t.Fatalf("rollup %v %v", b, err)
	}
}

func TestQueryDuringCheckpointDoesNotLoseSamples(t *testing.T) {
	s, err := Open(Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	start := time.Now().Unix() - 120
	for i := int64(0); i < 100; i++ {
		s.Append("query", start+i, float64(i))
	}
	done := make(chan error, 1)
	go func() { done <- s.Flush() }()
	for i := 0; i < 100; i++ {
		pts, err := s.Query("query", start, start+100)
		if err != nil || len(pts) != 100 {
			t.Fatalf("checkpoint made samples disappear: %d %v", len(pts), err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
