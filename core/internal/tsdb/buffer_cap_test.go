package tsdb

import (
	"io"
	"log/slog"
	"testing"
	"unsafe"
)

func TestActiveBufferCapStaysInsideTheBlock(t *testing.T) {
	s, err := Open(Options{
		Dir: t.TempDir(), BlockSize: 900,
		Tiers:  []TierSpec{{Every: 60, BlockSize: 720}, {Every: 3600, BlockSize: 360}},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	start := int64(1_700_000_000)
	for i := range 899 {
		s.Append("raw", start+int64(i), 1)
	}
	for i := range 720 {
		if s.tiers[0].append(s.tiers[0].getOrCreate("minute"), start+int64(i)*60, 1) {
			t.Fatal("minute block flushed before it was full")
		}
	}
	for i := range 360 {
		if s.tiers[1].append(s.tiers[1].getOrCreate("hour"), start+int64(i)*3600, 1) {
			t.Fatal("hour block flushed before it was full")
		}
	}
	raw := s.series["raw"]
	raw.mu.Lock()
	rawLen, rawCap, valCap := len(raw.ts), cap(raw.ts), cap(raw.vals)
	raw.mu.Unlock()
	minute := s.tiers[0].getOrCreate("minute")
	minute.mu.Lock()
	minuteLen, minuteCap := len(minute.done), cap(minute.done)
	minute.mu.Unlock()
	hour := s.tiers[1].getOrCreate("hour")
	hour.mu.Lock()
	hourLen, hourCap := len(hour.done), cap(hour.done)
	hour.mu.Unlock()
	if rawLen != 899 || rawCap != 900 || valCap != 900 {
		t.Fatalf("raw len=%d cap=%d/%d", rawLen, rawCap, valCap)
	}
	if minuteLen != 719 || minuteCap != 720 || hourLen != 359 || hourCap != 360 {
		t.Fatalf("minute %d/%d hour %d/%d", minuteLen, minuteCap, hourLen, hourCap)
	}
	// A doubling append would stop at 1024, 1024 and 512. The difference is
	// unused capacity on one series whose raw, minute and hour buffers are full.
	bucketBytes := int(unsafe.Sizeof(Bucket{}))
	if bucketBytes != 48 {
		t.Fatalf("rollup bucket is %d bytes", bucketBytes)
	}
	removed := (1024-rawCap)*16 + (1024-minuteCap)*bucketBytes + (512-hourCap)*bucketBytes
	if removed != 23872 {
		t.Fatalf("removed unused capacity = %d bytes", removed)
	}
}

func TestOverflowPastTheBlockDoesNotDouble(t *testing.T) {
	buf := appendCapped(make([]int64, 900, 900), 1, 900)
	if len(buf) != 901 || cap(buf) != 901 {
		t.Fatalf("len=%d cap=%d", len(buf), cap(buf))
	}
}

func TestCheckpointRestoreStaysInsideConfiguredBlocks(t *testing.T) {
	s, err := Open(Options{
		Dir: t.TempDir(), BlockSize: 900,
		Tiers:  []TierSpec{{Every: 60, BlockSize: 720}},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const n = 700
	ts, vals := make([]int64, n), make([]float64, n)
	buckets := make([]Bucket, n)
	for i := range n {
		ts[i], vals[i] = int64(i+1), 1
		buckets[i].TS = int64(i+1) * 60
	}
	if err := s.applyCheckpointImage(checkpointImage{
		Version: 1,
		Raw:     []rawCheckpoint{{ID: "x", Last: ts[n-1], TS: ts, Values: vals}},
		Tiers:   []tierCheckpoint{{Every: 60, Series: []rollupCheckpoint{{ID: "x", Done: buckets}}}},
	}); err != nil {
		t.Fatal(err)
	}
	raw := s.series["x"]
	raw.mu.Lock()
	rawLen, rawCap := len(raw.ts), cap(raw.ts)
	raw.mu.Unlock()
	roll := s.tiers[0].getOrCreate("x")
	roll.mu.Lock()
	rollLen, rollCap := len(roll.done), cap(roll.done)
	roll.mu.Unlock()
	if rawLen != n || rawCap != 900 || rollLen != n || rollCap != 720 {
		t.Fatalf("restored raw %d/%d rollup %d/%d", rawLen, rawCap, rollLen, rollCap)
	}
}
