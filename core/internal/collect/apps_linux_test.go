//go:build linux

package collect

import (
	"os"
	"testing"
)

func TestParseProcPIDStat(t *testing.T) {
	raw := []byte("12 (kworker/0:1) S 2 0 0 0 -1 0 0 0 0 0 10 20 0 0 20 0 4 0 0 0 7 0\n")
	name, ppid, ut, st, rss, thr, kthread, ok := parseProcPIDStat(raw)
	if !ok || name != "kworker/0:1" || ppid != 2 || ut != 10 || st != 20 || thr != 4 || rss != 7 || kthread {
		t.Fatalf("got name=%q ppid=%d ut=%d st=%d rss=%d thr=%d kthread=%v ok=%v", name, ppid, ut, st, rss, thr, kthread, ok)
	}
	kraw := []byte("3 (ksoftirqd/0) S 2 0 0 0 -1 2097152 0 0 0 0 1 2 0 0 20 0 1 0 0 0 0 0\n")
	_, _, _, _, _, _, kthread, ok = parseProcPIDStat(kraw)
	if !ok || !kthread {
		t.Fatalf("kthread ok=%v flag=%v", ok, kthread)
	}
	rd, wr, iok := parseProcIO([]byte("rchar: 1\nread_bytes: 100\nwrite_bytes: 50\n"))
	if !iok || rd != 100 || wr != 50 {
		t.Fatalf("io %d %d %v", rd, wr, iok)
	}
}

func TestReadProcSampleSelf(t *testing.T) {
	var buf []byte
	s := readProcSample(int32(os.Getpid()), false, &buf)
	if !s.ok || s.name == "" || s.threads < 1 {
		t.Fatalf("%+v", s)
	}
	var dir []byte
	pids, ok := listProcPIDs(nil, &dir)
	if !ok || len(pids) == 0 {
		t.Fatal("no pids")
	}
	self := int32(os.Getpid())
	for _, pid := range pids {
		if pid == self {
			return
		}
	}
	t.Fatalf("self pid %d missing from %d entries", self, len(pids))
}
