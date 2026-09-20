package collect

import (
	"runtime"
	"testing"

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
