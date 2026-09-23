//go:build linux

package collect

import "testing"

func TestParseProcPIDStat(t *testing.T) {
	raw := []byte("12 (kworker/0:1) S 2 0 0 0 -1 0 0 0 0 0 10 20 0 0 20 0 4 0 0 0 0 0 0\n")
	ut, st, thr, ok := parseProcPIDStat(raw)
	if !ok || ut != 10 || st != 20 || thr != 4 {
		t.Fatalf("got ut=%d st=%d thr=%d ok=%v", ut, st, thr, ok)
	}
	rd, wr, iok := parseProcIO([]byte("rchar: 1\nread_bytes: 100\nwrite_bytes: 50\n"))
	if !iok || rd != 100 || wr != 50 {
		t.Fatalf("io %d %d %v", rd, wr, iok)
	}
}
