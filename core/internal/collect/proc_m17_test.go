package collect

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestParseSCTPSnmp(t *testing.T) {
	m := parseSCTPSnmp("SctpCurrEstab 4\nSctpActiveEstabs 10\nSctpInSCTPPacks 100\n")
	if m["SctpCurrEstab"] != 4 || m["SctpActiveEstabs"] != 10 || m["SctpInSCTPPacks"] != 100 {
		t.Fatalf("%v", m)
	}
}

func TestParseSynproxy(t *testing.T) {
	raw := "entries syn_received cookie_invalid cookie_valid cookie_retrans conn_reopened\n" +
		"00000000 0000000a 00000001 00000008 00000002 00000003\n"
	st := parseSynproxy(raw)
	if st["syn_received"] != 10 || st["cookie_valid"] != 8 || st["conn_reopened"] != 3 {
		t.Fatalf("%v", st)
	}
}

func TestParsePageType(t *testing.T) {
	raw := "Page block order: 9\nFree pages count per migrate type at order 0 ...\n" +
		"Node    0, zone      DMA, type    Unmovable      1    2    3    0\n" +
		"Node    0, zone      DMA, type    Movable        4    0    0    0\n" +
		"Node    0, zone   Normal, type    Unmovable      10   0\n"
	z := parsePageType(raw)
	if z["DMA"]["Unmovable"] != 6 || z["DMA"]["Movable"] != 4 || z["Normal"]["Unmovable"] != 10 {
		t.Fatalf("%v", z)
	}
}

func TestParseInterruptsAndSoftirqs(t *testing.T) {
	raw := "           CPU0       CPU1\n" +
		"  0:         12          0   IO-APIC   2-edge      timer\n" +
		"NMI:          1          1   Non-maskable interrupts\n"
	ir := parseInterrupts(raw)
	if ir.byName["timer"] != 12 || ir.byName["NMI"] != 2 {
		t.Fatalf("byName=%v", ir.byName)
	}
	if len(ir.byCPU) != 2 || ir.byCPU[0] != 13 || ir.byCPU[1] != 1 {
		t.Fatalf("byCPU=%v", ir.byCPU)
	}
	si := parseSoftirqs("                CPU0       CPU1\n      HI:          1          2\n   TIMER:         10         20\n NET_RX:          3          4\n")
	if si["HI"] != 3 || si["TIMER"] != 30 || si["NET_RX"] != 7 {
		t.Fatalf("%v", si)
	}
}

func TestParseTC(t *testing.T) {
	raw := "qdisc htb 1: dev eth0 root refcnt 2\n" +
		" Sent 1234 bytes 5 pkt (dropped 2, overlimits 7 requeues 0)\n" +
		"qdisc fq_codel 0: dev lo root\n" +
		" Sent 99 bytes 1 pkt (dropped 0, overlimits 0 requeues 0)\n"
	qs := parseTC(raw)
	if len(qs) != 2 || qs[0].Dev != "eth0" || qs[0].Kind != "htb" || qs[0].Bytes != 1234 || qs[0].Dropped != 2 || qs[0].Overlimits != 7 {
		t.Fatalf("%+v", qs)
	}
	if qs[1].Dev != "lo" || qs[1].Packets != 1 {
		t.Fatalf("%+v", qs[1])
	}
}

func TestProcM17Fixture(t *testing.T) {
	root := t.TempDir()
	ib := filepath.Join(root, "infiniband", "mlx5_0", "ports", "1", "counters")
	if err := os.MkdirAll(ib, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, v string) {
		if err := os.WriteFile(filepath.Join(ib, name), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("port_xmit_data", "100")
	write("port_rcv_data", "200")
	write("port_xmit_packets", "3")
	write("port_rcv_packets", "4")
	write("port_xmit_discards", "1")
	write("symbol_error", "2")
	write("link_error_recovery", "0")

	numa := filepath.Join(root, "node", "node0")
	if err := os.MkdirAll(numa, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(numa, "numastat"), []byte("numa_hit 10\nnuma_miss 2\nnuma_foreign 1\ninterleave_hit 0\nlocal_node 9\nother_node 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sctp := filepath.Join(root, "sctp")
	if err := os.WriteFile(sctp, []byte("SctpCurrEstab 2\nSctpActiveEstabs 5\nSctpPassiveEstabs 1\nSctpAborteds 0\nSctpShutdowns 1\nSctpInSCTPPacks 8\nSctpOutSCTPPacks 9\nSctpInInvalid 0\nSctpInPktDiscards 1\nSctpChecksumErrors 0\nSctpReasmUsrMsgs 2\nSctpFragUsrMsgs 3\nSctpOutCtrlChunks 4\nSctpInCtrlChunks 5\nSctpOutOrderChunks 6\nSctpInOrderChunks 7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	syn := filepath.Join(root, "synproxy")
	if err := os.WriteFile(syn, []byte("entries syn_received cookie_invalid cookie_valid cookie_retrans conn_reopened\n00000000 0000000a 00000001 00000008 00000002 00000003\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snmp := filepath.Join(root, "snmp")
	if err := os.WriteFile(snmp, []byte("UdpLite: InDatagrams NoPorts InErrors OutDatagrams RcvbufErrors SndbufErrors InCsumErrors\nUdpLite: 11 1 2 12 0 0 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(root, "pagetype")
	if err := os.WriteFile(page, []byte("Node    0, zone      DMA, type    Unmovable      1    2\nNode    0, zone      DMA, type    Movable        4    0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	intr := filepath.Join(root, "interrupts")
	if err := os.WriteFile(intr, []byte("           CPU0       CPU1\n  0:         12          1   IO-APIC   2-edge      timer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	soft := filepath.Join(root, "softirqs")
	if err := os.WriteFile(soft, []byte("                CPU0       CPU1\n      HI:          1          2\n NET_RX:          3          4\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	p := &procCollector{}
	p.m17.ibRoot = filepath.Join(root, "infiniband")
	p.m17.numaRoot = filepath.Join(root, "node")
	p.m17.sctp = sctp
	p.m17.synproxy = syn
	p.m17.snmp = snmp
	p.m17.pagetype = page
	p.m17.interrupts = intr
	p.m17.softirqs = soft
	p.m17.tcDump = "qdisc htb 1: dev eth0 root\n Sent 50 bytes 2 pkt (dropped 1, overlimits 0 requeues 0)\n"
	p.initM17(reg)
	if !p.m17.any() {
		t.Fatal("expected m17 sources")
	}
	now := time.Unix(1_700_000_000, 0)
	p.collectM17(reg, now)
	p.collectM17(reg, now.Add(time.Second)) // incremental dims need two ticks

	must := func(id, dim string, want float64) {
		t.Helper()
		c, ok := reg.Chart(id)
		if !ok {
			t.Fatalf("missing chart %s", id)
		}
		_, vals := c.LastValues()
		if vals[dim] != want {
			t.Fatalf("%s.%s = %v want %v (vals=%v)", id, dim, vals[dim], want, vals)
		}
	}
	must("sctp.established", "established", 2)
	must("ipv4.udplitepackets", "received", 0) // incremental: second tick delta 0
	must("netfilter.synproxy_syn_received", "received", 0)
	must("mem.pagetype_DMA", "Unmovable", 3)
	must("ib.port_bytes.mlx5_0_1", "received", 0) // incremental
	if _, ok := reg.Chart("tc.qos_bytes.eth0_1"); !ok {
		t.Fatal("missing tc chart")
	}
	if _, ok := reg.Chart("mem.numa_events.node0"); !ok {
		t.Fatal("missing numa chart")
	}
	if _, ok := reg.Chart("system.interrupts"); !ok {
		t.Fatal("missing interrupts")
	}
	if _, ok := reg.Chart("system.softirqs"); !ok {
		t.Fatal("missing softirqs")
	}
}
