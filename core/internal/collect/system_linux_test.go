//go:build linux

package collect

import (
	"bytes"
	"testing"
)

func TestParseCPUStatSkipsAggregate(t *testing.T) {
	raw := []byte("cpu 100 0 50 200 10 1 2 3 4 5\ncpu0 40 0 20 100 5 1 1 1 2 3\ncpu1 60 0 30 100 5 0 1 2 2 2\nintr 9 1\n")
	end, ok := cpuPrefixEnd(raw)
	if !ok || !bytes.HasPrefix(raw[end:], []byte("intr")) {
		t.Fatalf("end=%d ok=%v tail=%q", end, ok, raw[end:])
	}
	got := parseCPUStat(raw[:end], nil)
	if len(got) != 2 || got[0].User != 0.4 || got[1].User != 0.6 || got[0].Guest != 0.02 || got[1].GuestNice != 0.02 {
		t.Fatalf("%+v", got)
	}
}
