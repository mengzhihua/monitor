//go:build linux

package collect

import (
	"bytes"
	"context"
	"math"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestLinuxMemoryMatchesMeminfo(t *testing.T) {
	kb := readMeminfoKB()
	if kb == nil || kb["Committed_AS"] == 0 || kb["MemTotal"] == 0 {
		t.Fatal("meminfo missing Committed_AS or MemTotal")
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	c := &memCollector{}
	if err := c.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	kb = readMeminfoKB()
	near := func(name string, got, want, tol float64) {
		t.Helper()
		if math.Abs(got-want) > tol {
			t.Fatalf("%s = %.2f MiB, meminfo %.2f MiB", name, got, want)
		}
	}
	mib := func(k uint64) float64 { return float64(k) / 1024 }
	committed, ok := reg.Chart("mem.committed")
	if !ok {
		t.Fatal("missing mem.committed")
	}
	_, last := committed.LastValues()
	near("committed", last["committed"], mib(kb["Committed_AS"]), 8)
	if overcommitMemoryMode() != 2 {
		if _, ok := last["limit"]; ok {
			t.Fatal("heuristic host published CommitLimit")
		}
	}
	ram, ok := reg.Chart("system.ram")
	if !ok {
		t.Fatal("missing system.ram")
	}
	_, ramLast := ram.LastValues()
	reclaim := kb["SReclaimable"]
	if kb["KReclaimable"] > 0 {
		reclaim = kb["KReclaimable"]
	}
	cachedKB := kb["Cached"] + reclaim - kb["Shmem"]
	usedKB := kb["MemTotal"] - kb["MemFree"] - cachedKB - kb["Buffers"]
	near("used", ramLast["used"], mib(usedKB), 8)
	near("cached", ramLast["cached"], mib(cachedKB), 8)
	avail, ok := reg.Chart("mem.available")
	if !ok {
		t.Fatal("missing mem.available")
	}
	_, availLast := avail.LastValues()
	near("available", availLast["avail"], mib(kb["MemAvailable"]), 8)
	kernel, ok := reg.Chart("mem.kernel")
	if !ok {
		t.Fatal("missing mem.kernel")
	}
	if kernel.Dimension("sunreclaim") != nil {
		t.Fatal("unreclaimable slab is already inside slab")
	}
	_, kernLast := kernel.LastValues()
	near("slab", kernLast["slab"], mib(kb["Slab"]), 8)
	near("page_tables", kernLast["page_tables"], mib(kb["PageTables"]), 8)
	// These counters are only a few MiB. A loose tolerance would hide a 1024x unit error.
	near("kernelstack", kernLast["kernelstack"], mib(kb["KernelStack"]), 0.5)
	near("percpu", kernLast["percpu"], mib(kb["Percpu"]), 0.5)
}

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
