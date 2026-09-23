package tsdb

import (
	"os"
	"os/exec"
	"path/filepath"
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

// A torn tail used to stay in the file. The next run appended after it, and the
// run after that stopped at the tear, so every sample taken since the tear was
// never replayed.
func TestTornWALTailDoesNotHideLaterSamples(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ts := int64(1_700_000_000)
	s.Append("wal", ts, 1)
	if s.wal == nil || s.wal.sync() != nil {
		t.Fatal("sync")
	}
	stopWithoutCheckpoint(s)

	f, err := os.OpenFile(filepath.Join(dir, walFile), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte{0x04, 0x00, 'x'}); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	s.Append("wal", ts+1, 2)
	if s.wal == nil || s.wal.sync() != nil {
		t.Fatal("sync")
	}
	stopWithoutCheckpoint(s)

	s, err = Open(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pts, err := s.Query("wal", ts, ts+1)
	if err != nil || len(pts) != 2 || pts[0].Value != 1 || pts[1].Value != 2 {
		t.Fatalf("post-tear sample lost: %v %v", pts, err)
	}
}
