package collect

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestParseSlabinfo(t *testing.T) {
	raw := "slabinfo - version: 2.1\n# name            <active_objs> <num_objs> <objsize> <objperslab> <pagesperslab>\n" +
		"kmalloc-8             10    20     8   512    1 : tunables 0\n" +
		"dentry                4     8    192    21    1 : tunables 0\n"
	st := parseSlabinfo(raw)
	if st.ActiveObjs != 14 || st.TotalObjs != 28 || st.ActiveBytes != 10*8+4*192 || st.TotalBytes != 20*8+8*192 {
		t.Fatalf("%+v", st)
	}
}

func TestProcM18Fixture(t *testing.T) {
	root := t.TempDir()
	mc := filepath.Join(root, "edac", "mc0")
	if err := os.MkdirAll(filepath.Join(mc, "dimm0"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, v string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(mc, "ce_count"), "3")
	write(filepath.Join(mc, "ue_count"), "1")
	write(filepath.Join(mc, "dimm0", "dimm_ce_count"), "2")
	write(filepath.Join(mc, "dimm0", "dimm_ue_count"), "0")

	slab := filepath.Join(root, "slabinfo")
	write(slab, "slabinfo - version: 2.1\n# name            <active_objs> <num_objs> <objsize>\nkmalloc-8  10  20  8\n")

	zswap := filepath.Join(root, "zswap")
	if err := os.MkdirAll(zswap, 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(zswap, "pool_total_size"), "4096")
	write(filepath.Join(zswap, "pool_limit_hit"), "1")
	write(filepath.Join(zswap, "written_back_pages"), "2")
	write(filepath.Join(zswap, "reject_reclaim_fail"), "0")
	write(filepath.Join(zswap, "reject_alloc_fail"), "0")

	rapl := filepath.Join(root, "powercap", "intel-rapl-0")
	if err := os.MkdirAll(rapl, 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(rapl, "name"), "package-0")
	write(filepath.Join(rapl, "energy_uj"), "2000000")

	drm := filepath.Join(root, "drm", "card0", "device")
	if err := os.MkdirAll(drm, 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(drm, "gpu_busy_percent"), "42")
	write(filepath.Join(root, "drm", "card0", "gt_cur_freq_mhz"), "800")

	bc := filepath.Join(root, "bcache", "uuid-1", "stats_total")
	if err := os.MkdirAll(bc, 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(bc, "cache_hits"), "9")
	write(filepath.Join(bc, "cache_misses"), "1")

	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	p := &procCollector{}
	p.m18.edacRoot = filepath.Join(root, "edac")
	p.m18.slab = slab
	p.m18.zswapRoot = zswap
	p.m18.powercapRoot = filepath.Join(root, "powercap")
	p.m18.drmRoot = filepath.Join(root, "drm")
	p.m18.bcacheRoot = filepath.Join(root, "bcache")
	p.m18.timex = func() (float64, float64, float64, bool) { return 0, 0, 12.5, true }
	p.initM18(reg)
	if !p.m18.any() {
		t.Fatal("expected m18 sources")
	}
	now := time.Unix(1_700_000_000, 0)
	p.collectM18(reg, now)
	p.collectM18(reg, now.Add(time.Second))

	must := func(id, dim string) {
		t.Helper()
		c, ok := reg.Chart(id)
		if !ok {
			t.Fatalf("missing %s", id)
		}
		_, vals := c.LastValues()
		if _, ok := vals[dim]; !ok {
			t.Fatalf("%s missing dim %s vals=%v", id, dim, vals)
		}
	}
	must("mem.slabmemory", "active")
	must("mem.edac_mc.mc0", "ce")
	must("mem.zswap", "pool")
	must("cpu.powercap.package-0", "power")
	must("drm.gpu_busy.card0", "busy")
	must("drm.gpu_freq.card0", "freq")
	must("bcache.hits.uuid-1", "hits")
	must("system.clock_sync_offset", "offset")
	c, _ := reg.Chart("mem.slabmemory")
	_, vals := c.LastValues()
	if vals["active"] != 80 {
		t.Fatalf("slab active bytes %v", vals)
	}
	c, _ = reg.Chart("system.clock_sync_offset")
	_, vals = c.LastValues()
	if vals["offset"] != 12.5 {
		t.Fatalf("offset %v", vals)
	}
}
