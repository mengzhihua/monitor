package tsdb

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
)

func TestWALCrashRecovery(t *testing.T) {
	if dir := os.Getenv("MONITOR_WAL_DIR"); dir != "" {
		s, err := Open(Options{Dir: dir})
		if err != nil {
			os.Exit(2)
		}
		ts, _ := strconv.ParseInt(os.Getenv("MONITOR_WAL_TS"), 10, 64)
		s.Append("wal", ts, 7)
		if err := s.Flush(); err != nil {
			os.Exit(3)
		}
		s.Append("wal", ts+1, 8)
		if s.wal == nil || s.wal.sync() != nil {
			os.Exit(4)
		}
		os.Exit(0)
	}
	dir := t.TempDir()
	ts := int64(1_700_000_000)
	cmd := exec.Command(os.Args[0], "-test.run=^TestWALCrashRecovery$")
	cmd.Env = append(os.Environ(),
		"MONITOR_WAL_DIR="+dir,
		"MONITOR_WAL_TS="+strconv.FormatInt(ts, 10),
	)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, b)
	}
	s, err := Open(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pts, err := s.Query("wal", ts, ts+1)
	if err != nil || len(pts) != 2 || pts[0].Value != 7 || pts[1].Value != 8 {
		t.Fatalf("replay %v %v", pts, err)
	}
}
