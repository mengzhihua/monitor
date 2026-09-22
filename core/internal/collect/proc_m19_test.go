package collect

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestParseExtfragAndAuditctl(t *testing.T) {
	zs := parseExtfrag("Node 0, zone      DMA -1.000 0.200 0.800\nNode 0, zone   Normal 0.100 0.050\n")
	if len(zs) != 2 || zs[0].ID != "0_DMA" || zs[0].Index != 0.8 || zs[1].ID != "0_Normal" {
		t.Fatalf("%+v", zs)
	}
	st, ok := parseAuditctl("enabled 1\nfailure 1\nbacklog 4\nbacklog_limit 80\nlost 2\n")
	if !ok || st.Enabled != 1 || st.Backlog != 4 || st.BacklogLimit != 80 || st.Lost != 2 {
		t.Fatalf("%+v", st)
	}
}

func TestProcM19Fixture(t *testing.T) {
	root := t.TempDir()
	ext := filepath.Join(root, "extfrag_index")
	if err := os.WriteFile(ext, []byte("Node 0, zone DMA 0.25 0.50\nNode 1, zone Normal 0.10\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	p := &procCollector{}
	p.m19.extfrag = ext
	p.m19.audit = func() (auditStatus, bool) {
		return auditStatus{Enabled: 1, Failure: 1, Backlog: 10, BacklogLimit: 100, Lost: 3}, true
	}
	p.initM19(reg)
	if !p.m19.haveExtfrag || !p.m19.haveAudit {
		t.Fatalf("flags %+v", p.m19)
	}
	now := time.Unix(1_700_000_000, 0)
	p.collectM19(reg, now)
	p.collectM19(reg, now.Add(time.Second))
	if _, ok := reg.Chart("mem.extfrag.0_DMA"); !ok {
		t.Fatal("missing extfrag chart")
	}
	ch, ok := reg.Chart("audit.backlog")
	if !ok {
		t.Fatal("missing audit.backlog")
	}
	_, vals := ch.LastValues()
	if vals["used"] != 10 || vals["free"] != 90 {
		t.Fatalf("%v", vals)
	}
	ch, _ = reg.Chart("audit.backlog_utilization")
	_, vals = ch.LastValues()
	if vals["utilization"] != 10 {
		t.Fatalf("util %v", vals)
	}
}
