package collect

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestMountIDDistinct(t *testing.T) {
	seen := map[string]string{}
	for _, mp := range []string{"/", "/srv/a_b", "/srv/a/b", "/srv/a__b", "/mnt/c d", `C:\`, "/boot/efi"} {
		id := mountID(mp)
		if id == "" {
			t.Fatalf("empty id for %q", mp)
		}
		if prev, dup := seen[id]; dup {
			t.Fatalf("%q and %q both map to %q", prev, mp, id)
		}
		seen[id] = mp
	}
}

func TestCPURawGuest(t *testing.T) {
	raw := cpuRaw(cpu.TimesStat{User: 100, Nice: 20, Guest: 30, GuestNice: 5})
	if raw["guest"] != 30 || raw["guest_nice"] != 5 {
		t.Fatalf("guest dims = %v", raw)
	}
	wantUser, wantNice := 100.0, 20.0
	if runtime.GOOS == "linux" {
		wantUser, wantNice = 70, 15
	}
	if raw["user"] != wantUser || raw["nice"] != wantNice {
		t.Fatalf("user/nice = %v/%v, want %v/%v", raw["user"], raw["nice"], wantUser, wantNice)
	}
}

func TestLinuxCommitLimitEnforcedOnlyInStrictMode(t *testing.T) {
	if linuxCommitLimitEnforced(0) || linuxCommitLimitEnforced(1) || !linuxCommitLimitEnforced(2) {
		t.Fatal("only overcommit mode 2 enforces CommitLimit")
	}
}

func TestMemCommittedSkipsDecorativeLimit(t *testing.T) {
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	c := &memCollector{}
	if err := c.Init(reg); err != nil {
		t.Fatal(err)
	}
	chart, ok := reg.Chart("mem.committed")
	if !ok {
		t.Fatal("missing mem.committed")
	}
	if runtime.GOOS == "linux" && overcommitMemoryMode() != 2 {
		if chart.Dimension("limit") != nil {
			t.Fatal("heuristic overcommit must not publish CommitLimit")
		}
		if chart.Labels["commit_limit"] != "" {
			t.Fatal("heuristic overcommit must not arm committed_memory")
		}
	}
	if linuxCommitLimitEnforced(overcommitMemoryMode()) && runtime.GOOS == "linux" {
		if chart.Dimension("limit") == nil || chart.Labels["commit_limit"] != "enforced" {
			t.Fatalf("strict overcommit chart = dims limit:%v labels:%v", chart.Dimension("limit") != nil, chart.Labels)
		}
	}
	now := time.Unix(1_700_000_000, 0)
	if err := c.Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	_, last := chart.LastValues()
	if _, ok := last["committed"]; !ok || last["committed"] < 0 {
		t.Fatalf("committed sample = %v", last)
	}
	if _, ok := last["limit"]; ok != (chart.Dimension("limit") != nil) {
		t.Fatalf("limit sample present=%v dimension=%v", ok, chart.Dimension("limit") != nil)
	}
}

// gopsutil SwapMemoryStat.PgFault/PgMajFault are byte counts (pages × 4096),
// while mem.pgfaults is in faults/s (pages). The dimensions must divide by
// the page size, otherwise rates are inflated 4096× and 1m_major_page_faults
// fires on healthy hosts (one real major fault → 4096/61s ≈ 67 faults/s).
func TestMemPgfaultsRateInPages(t *testing.T) {
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	c := &memCollector{}
	if err := c.Init(reg); err != nil {
		t.Fatal(err)
	}
	chart, ok := reg.Chart("mem.pgfaults")
	if !ok {
		t.Fatal("missing mem.pgfaults")
	}
	for _, id := range []string{"minor", "major"} {
		if d := chart.Dimension(id); d == nil || d.Divisor != 4096 {
			t.Fatalf("dim %s divisor = %v, want 4096 (bytes→pages)", id, d)
		}
	}
	// Feed gopsutil-scale byte counters: +4096 bytes/s = 1 page/s.
	now := time.Unix(1_700_000_000, 0)
	_ = reg.Collect("mem.pgfaults", now, map[string]float64{"minor": 1 << 30, "major": 341878 * 4096})
	_ = reg.Collect("mem.pgfaults", now.Add(time.Second),
		map[string]float64{"minor": 1<<30 + 4096, "major": 341878*4096 + 4096})
	_, last := chart.LastValues()
	if last["major"] != 1 {
		t.Fatalf("major rate = %v, want 1 fault/s (page semantics)", last["major"])
	}
	if last["minor"] != 1 {
		t.Fatalf("minor rate = %v, want 1 fault/s (page semantics)", last["minor"])
	}
}

func TestLoadEveryNeverBelowScheduler(t *testing.T) {
	reg := registry.New(&registry.Host{UpdateEvery: 15}, nil)
	if got := loadEvery(reg); got != 15 {
		t.Fatalf("loadEvery = %d, want 15", got)
	}
	reg = registry.New(&registry.Host{UpdateEvery: 1}, nil)
	if got := loadEvery(reg); got != 5 {
		t.Fatalf("loadEvery = %d, want 5", got)
	}
}

func TestSystemFunctions(t *testing.T) {
	ctx := context.Background()
	if tab, err := (&diskCollector{}).Functions()[0].Run(ctx, nil); err != nil {
		t.Fatal("disks", err)
	} else if t0, ok := tab.(Table); !ok || t0.Total == 0 {
		t.Fatalf("disks table = %+v", tab)
	}
	if tab, err := (&diskSpaceCollector{}).Functions()[0].Run(ctx, nil); err != nil {
		t.Fatal("mounts", err)
	} else if t0, ok := tab.(Table); !ok || len(t0.Columns) == 0 {
		t.Fatalf("mounts table = %+v", tab)
	}
	if tab, err := (&netCollector{}).Functions()[0].Run(ctx, nil); err != nil {
		t.Fatal("ifaces", err)
	} else if t0, ok := tab.(Table); !ok || t0.Total == 0 {
		t.Fatalf("ifaces table = %+v", tab)
	}
}
