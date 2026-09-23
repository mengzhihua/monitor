package collect

import (
	"bufio"
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// procM19 is leftover debugfs/audit after M18: NUMA extfrag index and
// kernel audit backlog (auditctl -s approximation of NETLINK_AUDIT).
type procM19 struct {
	haveExtfrag, haveAudit bool
	extfrag                string
	audit                  func() (auditStatus, bool)
	extSeen                map[string]bool
	extAt                  time.Time
	extZones               []extfragZone
	auditAt                time.Time
}

func (m procM19) any() bool { return m.haveExtfrag || m.haveAudit }

type auditStatus struct {
	Enabled, Failure            float64
	Backlog, BacklogLimit, Lost float64
}

func (p *procCollector) initM19(reg *registry.Registry) {
	m := &p.m19
	m.extfrag = firstNonEmpty(m.extfrag, "/sys/kernel/debug/extfrag/extfrag_index")
	m.extSeen = map[string]bool{}
	if m.audit == nil {
		m.audit = readAuditStatus
	}
	if raw, err := readTrim(m.extfrag); err == nil && strings.Contains(raw, "Node") {
		m.haveExtfrag = true
	}
	if _, ok := m.audit(); ok {
		m.haveAudit = true
		ch := sysChart("audit.backlog", "audit", "Audit backlog", "events", 1450,
			&registry.Dimension{ID: "used"}, &registry.Dimension{ID: "free", Hidden: true})
		ch.Plugin, ch.Module, ch.Type = "proc", "audit", registry.Stacked
		reg.AddChart(ch)
		util := sysChart("audit.backlog_utilization", "audit", "Audit backlog utilization", "percentage", 1451,
			&registry.Dimension{ID: "utilization"})
		util.Plugin, util.Module = "proc", "audit"
		reg.AddChart(util)
		lost := sysChart("audit.lost", "audit", "Audit lost events", "events/s", 1452, incDim("lost"))
		lost.Plugin, lost.Module = "proc", "audit"
		reg.AddChart(lost)
		en := sysChart("audit.enabled", "audit", "Audit enabled state", "state", 1453,
			&registry.Dimension{ID: "disabled"}, &registry.Dimension{ID: "enabled"}, &registry.Dimension{ID: "immutable"})
		en.Plugin, en.Module = "proc", "audit"
		reg.AddChart(en)
		fail := sysChart("audit.failure", "audit", "Audit failure mode", "state", 1454,
			&registry.Dimension{ID: "silent"}, &registry.Dimension{ID: "printk"}, &registry.Dimension{ID: "panic"})
		fail.Plugin, fail.Module = "proc", "audit"
		reg.AddChart(fail)
	}
}

func (p *procCollector) collectM19(reg *registry.Registry, now time.Time) {
	m := &p.m19
	if m.haveExtfrag && (m.extAt.IsZero() || now.Sub(m.extAt) >= slowSampleEvery) {
		if raw, err := readTrim(m.extfrag); err == nil {
			m.extZones = parseExtfrag(raw)
			m.extAt = now
			for _, z := range m.extZones {
				id := "mem.extfrag." + sanitizeID(z.ID)
				if !m.extSeen[id] {
					m.extSeen[id] = true
					ch := sysChart(id, "fragmentation", "NUMA extfrag "+z.ID, "index", 1440,
						&registry.Dimension{ID: "index"})
					ch.Plugin, ch.Module = "proc", "extfrag"
					reg.AddChart(ch)
				}
				_ = reg.Collect(id, now, map[string]float64{"index": z.Index})
			}
		}
	}
	if m.haveAudit {
		if !m.auditAt.IsZero() && now.Sub(m.auditAt) < slowSampleEvery {
			return
		}
		st, ok := m.audit()
		if !ok {
			return
		}
		m.auditAt = now
		free := st.BacklogLimit - st.Backlog
		if free < 0 {
			free = 0
		}
		util := 0.0
		if st.BacklogLimit > 0 {
			util = st.Backlog * 100 / st.BacklogLimit
		}
		_ = reg.Collect("audit.backlog", now, map[string]float64{"used": st.Backlog, "free": free})
		_ = reg.Collect("audit.backlog_utilization", now, map[string]float64{"utilization": util})
		_ = reg.Collect("audit.lost", now, map[string]float64{"lost": st.Lost})
		dis, en, imm := 0.0, 0.0, 0.0
		switch st.Enabled {
		case 1:
			en = 1
		case 2:
			imm = 1
		default:
			dis = 1
		}
		_ = reg.Collect("audit.enabled", now, map[string]float64{"disabled": dis, "enabled": en, "immutable": imm})
		sil, prk, pan := 0.0, 0.0, 0.0
		switch st.Failure {
		case 1:
			prk = 1
		case 2:
			pan = 1
		default:
			sil = 1
		}
		_ = reg.Collect("audit.failure", now, map[string]float64{"silent": sil, "printk": prk, "panic": pan})
	}
}

type extfragZone struct {
	ID    string
	Index float64
}

func parseExtfrag(s string) []extfragZone {
	var out []extfragZone
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "Node") {
			continue
		}
		node, zone := "0", "Normal"
		if f := strings.Fields(line); len(f) >= 4 {
			node = strings.Trim(f[1], ",")
			// "Node 0, zone DMA" or "Node 0, zone Normal"
			zi := 0
			for i, tok := range f {
				if tok == "zone" && i+1 < len(f) {
					zone = f[i+1]
					zi = i + 2
					break
				}
			}
			idx := -1.0
			for _, tok := range f[zi:] {
				v, err := strconv.ParseFloat(tok, 64)
				if err != nil {
					continue
				}
				if v > idx {
					idx = v
				}
			}
			if idx < 0 {
				idx = 0
			}
			out = append(out, extfragZone{ID: node + "_" + zone, Index: idx})
		}
	}
	return out
}

func parseAuditctl(s string) (auditStatus, bool) {
	var st auditStatus
	ok := false
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(strings.ReplaceAll(sc.Text(), "=", " "))
		if len(f) < 2 {
			continue
		}
		v, err := strconv.ParseFloat(f[1], 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "enabled":
			st.Enabled, ok = v, true
		case "failure":
			st.Failure, ok = v, true
		case "backlog":
			st.Backlog, ok = v, true
		case "backlog_limit":
			st.BacklogLimit, ok = v, true
		case "lost":
			st.Lost, ok = v, true
		}
	}
	return st, ok
}

func readAuditStatus() (auditStatus, bool) {
	if st, ok := readAuditNetlink(); ok {
		return st, true
	}
	return readAuditctl()
}

func readAuditctl() (auditStatus, bool) {
	raw, err := execRun(2*time.Second)(context.Background(), "auditctl", "-s")
	if err != nil {
		return auditStatus{}, false
	}
	return parseAuditctl(string(raw))
}
