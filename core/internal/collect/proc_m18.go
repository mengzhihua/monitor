package collect

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// procM18 is leftover Linux proc/debugfs surface after M17: EDAC ECC,
// SLAB allocator, zswap, RAPL powercap, DRM GPU busy/freq, bcache, adjtimex.
type procM18 struct {
	haveEDAC, haveSlab, haveZswap, havePowercap bool
	haveDRM, haveBcache, haveTimex              bool
	edacRoot, slab, zswapRoot, powercapRoot     string
	drmRoot, bcacheRoot                         string
	edacSeen, drmSeen, bcacheSeen, raplSeen     map[string]bool
	timex                                       func() (state, unsync, offset float64, ok bool)
	slabAt                                      time.Time
	slabTot                                     slabTotals
}

func (m procM18) any() bool {
	return m.haveEDAC || m.haveSlab || m.haveZswap || m.havePowercap ||
		m.haveDRM || m.haveBcache || m.haveTimex
}

func (p *procCollector) initM18(reg *registry.Registry) {
	m := &p.m18
	m.edacRoot = firstNonEmpty(m.edacRoot, "/sys/devices/system/edac/mc")
	m.slab = firstNonEmpty(m.slab, "/proc/slabinfo")
	m.zswapRoot = firstNonEmpty(m.zswapRoot, "/sys/kernel/mm/zswap")
	m.powercapRoot = firstNonEmpty(m.powercapRoot, "/sys/class/powercap")
	m.drmRoot = firstNonEmpty(m.drmRoot, "/sys/class/drm")
	m.bcacheRoot = firstNonEmpty(m.bcacheRoot, "/sys/fs/bcache")
	m.edacSeen, m.drmSeen, m.bcacheSeen, m.raplSeen = map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	if m.timex == nil {
		m.timex = readTimex
	}

	if ents, err := os.ReadDir(m.edacRoot); err == nil {
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), "mc") {
				if _, err := readTrim(filepath.Join(m.edacRoot, e.Name(), "ce_count")); err == nil {
					m.haveEDAC = true
					break
				}
			}
		}
	}
	if raw, err := readTrim(m.slab); err == nil && strings.Contains(raw, "active_objs") {
		m.haveSlab = true
		ch := sysChart("mem.slabmemory", "slab", "Kernel SLAB memory", "bytes", 1420,
			&registry.Dimension{ID: "active"}, &registry.Dimension{ID: "total"})
		ch.Plugin, ch.Module = "proc", "slabinfo"
		reg.AddChart(ch)
		obj := sysChart("mem.slab_objects", "slab", "Kernel SLAB objects", "objects", 1421,
			&registry.Dimension{ID: "active"}, &registry.Dimension{ID: "total"})
		obj.Plugin, obj.Module = "proc", "slabinfo"
		reg.AddChart(obj)
	}
	if _, err := readTrim(filepath.Join(m.zswapRoot, "pool_total_size")); err == nil {
		m.haveZswap = true
	} else if _, err := readTrim(filepath.Join(m.zswapRoot, "stored_pages")); err == nil {
		m.haveZswap = true
	}
	if m.haveZswap {
		usage := sysChart("mem.zswap", "zswap", "ZSwap pool size", "bytes", 1430, &registry.Dimension{ID: "pool"})
		usage.Plugin, usage.Module = "proc", "zswap"
		reg.AddChart(usage)
		act := sysChart("mem.zswap_activity", "zswap", "ZSwap activity", "events/s", 1431,
			incDim("pool_limit_hit"), incDim("written_back"), incDim("reject_reclaim_fail"), incDim("reject_alloc_fail"))
		act.Plugin, act.Module = "proc", "zswap"
		reg.AddChart(act)
	}
	if ents, err := os.ReadDir(m.powercapRoot); err == nil {
		for _, e := range ents {
			if _, err := readTrim(filepath.Join(m.powercapRoot, e.Name(), "energy_uj")); err == nil {
				m.havePowercap = true
				break
			}
		}
	}
	if ents, err := os.ReadDir(m.drmRoot); err == nil {
		for _, e := range ents {
			if !strings.HasPrefix(e.Name(), "card") || strings.Contains(e.Name(), "-") {
				continue
			}
			dev := filepath.Join(m.drmRoot, e.Name())
			if _, err := readTrim(filepath.Join(dev, "device", "gpu_busy_percent")); err == nil {
				m.haveDRM = true
				break
			}
			if _, err := readTrim(filepath.Join(dev, "gt_cur_freq_mhz")); err == nil {
				m.haveDRM = true
				break
			}
		}
	}
	if ents, err := os.ReadDir(m.bcacheRoot); err == nil {
		for _, e := range ents {
			if _, err := os.Stat(filepath.Join(m.bcacheRoot, e.Name(), "stats_total")); err == nil {
				m.haveBcache = true
				break
			}
		}
	}
	if _, unsync, _, ok := m.timex(); ok {
		m.haveTimex = true
		_ = unsync
		st := sysChart("system.clock_sync_state", "clock", "System clock sync state", "state", 250,
			&registry.Dimension{ID: "state"})
		st.Plugin, st.Module = "proc", "timex"
		reg.AddChart(st)
		us := sysChart("system.clock_status", "clock", "System clock unsynchronized", "boolean", 251,
			&registry.Dimension{ID: "unsync"})
		us.Plugin, us.Module = "proc", "timex"
		reg.AddChart(us)
		off := sysChart("system.clock_sync_offset", "clock", "System clock offset", "microseconds", 252,
			&registry.Dimension{ID: "offset"})
		off.Plugin, off.Module = "proc", "timex"
		reg.AddChart(off)
	}
}

func (p *procCollector) collectM18(reg *registry.Registry, now time.Time) {
	m := &p.m18
	if m.haveEDAC {
		p.collectEDAC(reg, now)
	}
	if m.haveSlab && (m.slabAt.IsZero() || now.Sub(m.slabAt) >= slowSampleEvery) {
		if raw, err := readTrim(m.slab); err == nil {
			m.slabTot = parseSlabinfo(raw)
			m.slabAt = now
			_ = reg.Collect("mem.slabmemory", now, map[string]float64{"active": m.slabTot.ActiveBytes, "total": m.slabTot.TotalBytes})
			_ = reg.Collect("mem.slab_objects", now, map[string]float64{"active": m.slabTot.ActiveObjs, "total": m.slabTot.TotalObjs})
		}
	}
	if m.haveZswap {
		pool, _ := readFloat(filepath.Join(m.zswapRoot, "pool_total_size"))
		_ = reg.Collect("mem.zswap", now, map[string]float64{"pool": pool})
		hit, _ := readFloat(filepath.Join(m.zswapRoot, "pool_limit_hit"))
		wb, _ := readFloat(filepath.Join(m.zswapRoot, "written_back_pages"))
		rr, _ := readFloat(filepath.Join(m.zswapRoot, "reject_reclaim_fail"))
		ra, _ := readFloat(filepath.Join(m.zswapRoot, "reject_alloc_fail"))
		_ = reg.Collect("mem.zswap_activity", now, map[string]float64{
			"pool_limit_hit": hit, "written_back": wb, "reject_reclaim_fail": rr, "reject_alloc_fail": ra})
	}
	if m.havePowercap {
		p.collectPowercap(reg, now)
	}
	if m.haveDRM {
		p.collectDRM(reg, now)
	}
	if m.haveBcache {
		p.collectBcache(reg, now)
	}
	if m.haveTimex {
		if state, unsync, offset, ok := m.timex(); ok {
			_ = reg.Collect("system.clock_sync_state", now, map[string]float64{"state": state})
			_ = reg.Collect("system.clock_status", now, map[string]float64{"unsync": unsync})
			_ = reg.Collect("system.clock_sync_offset", now, map[string]float64{"offset": offset})
		}
	}
}

func (p *procCollector) collectEDAC(reg *registry.Registry, now time.Time) {
	ents, err := os.ReadDir(p.m18.edacRoot)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), "mc") {
			continue
		}
		mc := e.Name()
		dir := filepath.Join(p.m18.edacRoot, mc)
		ce, err1 := readFloat(filepath.Join(dir, "ce_count"))
		ue, err2 := readFloat(filepath.Join(dir, "ue_count"))
		if err1 != nil && err2 != nil {
			continue
		}
		id := "mem.edac_mc." + mc
		if !p.m18.edacSeen[id] {
			p.m18.edacSeen[id] = true
			ch := sysChart(id, "edac", "EDAC memory controller "+mc, "errors", 1440, incDim("ce"), incDim("ue"))
			ch.Plugin, ch.Module, ch.Context = "proc", "edac", "mem.edac_mc"
			reg.AddChart(ch)
		}
		_ = reg.Collect(id, now, map[string]float64{"ce": ce, "ue": ue})
		subs, _ := os.ReadDir(dir)
		for _, dimm := range subs {
			if !strings.HasPrefix(dimm.Name(), "dimm") && !strings.HasPrefix(dimm.Name(), "rank") {
				continue
			}
			dce, e1 := readFloat(filepath.Join(dir, dimm.Name(), "dimm_ce_count"))
			due, e2 := readFloat(filepath.Join(dir, dimm.Name(), "dimm_ue_count"))
			if e1 != nil && e2 != nil {
				continue
			}
			did := "mem.edac_dimm." + mc + "_" + dimm.Name()
			if !p.m18.edacSeen[did] {
				p.m18.edacSeen[did] = true
				ch := sysChart(did, "edac", "EDAC DIMM "+mc+"/"+dimm.Name(), "errors", 1441, incDim("ce"), incDim("ue"))
				ch.Plugin, ch.Module, ch.Context = "proc", "edac", "mem.edac_dimm"
				reg.AddChart(ch)
			}
			_ = reg.Collect(did, now, map[string]float64{"ce": dce, "ue": due})
		}
	}
}

func (p *procCollector) collectPowercap(reg *registry.Registry, now time.Time) {
	ents, err := os.ReadDir(p.m18.powercapRoot)
	if err != nil {
		return
	}
	for _, e := range ents {
		dir := filepath.Join(p.m18.powercapRoot, e.Name())
		uj, err := readFloat(filepath.Join(dir, "energy_uj"))
		if err != nil {
			continue
		}
		name, _ := readTrim(filepath.Join(dir, "name"))
		if name == "" {
			name = e.Name()
		}
		id := "cpu.powercap." + sanitizeID(name)
		if !p.m18.raplSeen[id] {
			p.m18.raplSeen[id] = true
			// energy_uj incremental / 1e6 = Joules/s = Watts
			ch := sysChart(id, "powercap", "Powercap "+name, "Watts", 1450,
				&registry.Dimension{ID: "power", Algorithm: registry.Incremental, Divisor: 1_000_000})
			ch.Plugin, ch.Module, ch.Context = "proc", "powercap", "cpu.powercap"
			reg.AddChart(ch)
		}
		_ = reg.Collect(id, now, map[string]float64{"power": uj})
	}
}

func (p *procCollector) collectDRM(reg *registry.Registry, now time.Time) {
	ents, err := os.ReadDir(p.m18.drmRoot)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), "card") || strings.Contains(e.Name(), "-") {
			continue
		}
		card := e.Name()
		dev := filepath.Join(p.m18.drmRoot, card)
		busy, berr := readFloat(filepath.Join(dev, "device", "gpu_busy_percent"))
		freq, ferr := readFloat(filepath.Join(dev, "gt_cur_freq_mhz"))
		if berr != nil && ferr != nil {
			continue
		}
		if berr == nil {
			id := "drm.gpu_busy." + card
			if !p.m18.drmSeen[id] {
				p.m18.drmSeen[id] = true
				ch := sysChart(id, "drm", "GPU busy "+card, "percentage", 1460, &registry.Dimension{ID: "busy"})
				ch.Plugin, ch.Module, ch.Context = "proc", "drm", "drm.gpu_busy"
				reg.AddChart(ch)
			}
			_ = reg.Collect(id, now, map[string]float64{"busy": busy})
		}
		if ferr == nil {
			id := "drm.gpu_freq." + card
			if !p.m18.drmSeen[id] {
				p.m18.drmSeen[id] = true
				ch := sysChart(id, "drm", "GPU frequency "+card, "MHz", 1461, &registry.Dimension{ID: "freq"})
				ch.Plugin, ch.Module, ch.Context = "proc", "drm", "drm.gpu_freq"
				reg.AddChart(ch)
			}
			_ = reg.Collect(id, now, map[string]float64{"freq": freq})
		}
	}
}

func (p *procCollector) collectBcache(reg *registry.Registry, now time.Time) {
	ents, err := os.ReadDir(p.m18.bcacheRoot)
	if err != nil {
		return
	}
	for _, e := range ents {
		dir := filepath.Join(p.m18.bcacheRoot, e.Name(), "stats_total")
		hits, hErr := readFloat(filepath.Join(dir, "cache_hits"))
		miss, mErr := readFloat(filepath.Join(dir, "cache_misses"))
		if hErr != nil && mErr != nil {
			continue
		}
		id := "bcache.hits." + sanitizeID(e.Name())
		if !p.m18.bcacheSeen[id] {
			p.m18.bcacheSeen[id] = true
			ch := sysChart(id, "bcache", "bcache hits "+e.Name(), "events/s", 1470, incDim("hits"), incDim("misses"))
			ch.Plugin, ch.Module, ch.Context = "proc", "bcache", "bcache.hits"
			reg.AddChart(ch)
			ratio := sysChart("bcache.hit_ratio."+sanitizeID(e.Name()), "bcache", "bcache hit ratio "+e.Name(), "percentage", 1471,
				&registry.Dimension{ID: "ratio"})
			ratio.Plugin, ratio.Module, ratio.Context = "proc", "bcache", "bcache.hit_ratio"
			reg.AddChart(ratio)
		}
		_ = reg.Collect(id, now, map[string]float64{"hits": hits, "misses": miss})
		r := 0.0
		if hits+miss > 0 {
			r = 100 * hits / (hits + miss)
		}
		_ = reg.Collect("bcache.hit_ratio."+sanitizeID(e.Name()), now, map[string]float64{"ratio": r})
	}
}

type slabTotals struct {
	ActiveBytes, TotalBytes, ActiveObjs, TotalObjs float64
}

func parseSlabinfo(s string) slabTotals {
	var out slabTotals
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "slabinfo") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		active, _ := strconv.ParseFloat(f[1], 64)
		total, _ := strconv.ParseFloat(f[2], 64)
		objsize, _ := strconv.ParseFloat(f[3], 64)
		out.ActiveObjs += active
		out.TotalObjs += total
		out.ActiveBytes += active * objsize
		out.TotalBytes += total * objsize
	}
	return out
}
