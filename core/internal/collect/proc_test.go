package collect

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestParseProcStat(t *testing.T) {
	st := parseProcStat("cpu 1 2 3\nintr 12345 0 1\nctxt 9\nprocesses 77\n")
	if st.intr != 12345 || st.forks != 77 {
		t.Fatalf("%+v", st)
	}
}

func TestScanProcStatSkipsLongIntrLine(t *testing.T) {
	var b strings.Builder
	b.WriteString("cpu 1 2 3 4\nintr 12345")
	for i := 0; i < 8000; i++ {
		b.WriteString(" 1")
	}
	b.WriteString("\nctxt 9\nprocesses 77\n")
	st, err := scanProcStat(strings.NewReader(b.String()))
	if err != nil || st.intr != 12345 || st.forks != 77 {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestParseFileNR(t *testing.T) {
	a, u, m, err := parseFileNR("1234\t0\t922337")
	if err != nil || a != 1234 || u != 0 || m != 922337 {
		t.Fatalf("%v %v %v %v", a, u, m, err)
	}
	if _, _, _, err := parseFileNR("1 2"); err == nil {
		t.Fatal("expected error")
	}
}

func TestParsePressure(t *testing.T) {
	p := parsePressure("some avg10=1.50 avg60=2.00 avg300=3.25 total=1000\nfull avg10=0.10 avg60=0.20 avg300=0.30 total=50\n")
	if p.some10 != 1.5 || p.some60 != 2 || p.fullTotal != 50 {
		t.Fatalf("%+v", p)
	}
}

func TestParseSNMP(t *testing.T) {
	raw := "Tcp: RtoAlgorithm RtoMin CurrEstab InSegs OutSegs RetransSegs\n" +
		"Tcp: 1 200 10 100 80 3\n" +
		"Udp: InDatagrams NoPorts InErrors OutDatagrams\n" +
		"Udp: 5 1 0 7\n"
	m := parseSNMP(raw)
	if snmpGet(m, "Tcp", "CurrEstab") != 10 || snmpGet(m, "Udp", "OutDatagrams") != 7 {
		t.Fatalf("%v", m)
	}
}

func TestParseApacheStatus(t *testing.T) {
	body := "Total Accesses: 42\nTotal kBytes: 8\nBusyWorkers: 3\nIdleWorkers: 7\n" +
		"ConnsTotal: 2\nConnsAsyncWriting: 0\nConnsAsyncKeepAlive: 1\nConnsAsyncClosing: 0\n" +
		"Scoreboard: ___W_K..\n"
	st, err := parseApacheStatus(body)
	if err != nil {
		t.Fatal(err)
	}
	if st.num["BusyWorkers"] != 3 || st.num["Total Accesses"] != 42 {
		t.Fatalf("%v", st.num)
	}
	sb := parseApacheScoreboard(st.scoreboard)
	if sb["waiting"] != 4 || sb["sending"] != 1 || sb["keepalive"] != 1 || sb["open"] != 2 {
		t.Fatalf("%v", sb)
	}
	if _, err := parseApacheStatus("not apache"); err == nil {
		t.Fatal("expected error")
	}
}

func TestParsePHPFPMStatus(t *testing.T) {
	m, err := parsePHPFPMStatus("pool: www\naccepted conn: 10\nlisten queue: 1\nmax listen queue: 2\nidle processes: 3\nactive processes: 1\nslow requests: 4\n")
	if err != nil || m["accepted conn"] != 10 || m["active processes"] != 1 {
		t.Fatalf("%v %v", m, err)
	}
}

func TestParseStatsDLine(t *testing.T) {
	g, c, tm := map[string]float64{}, map[string]float64{}, map[string][]float64{}
	parseStatsDLine(g, c, tm, "hits:2|c|@0.5")
	parseStatsDLine(g, c, tm, "temp:21|g")
	parseStatsDLine(g, c, tm, "temp:+1|g")
	parseStatsDLine(g, c, tm, "api:12|ms")
	if c["hits"] != 4 || g["temp"] != 22 || len(tm["api"]) != 1 {
		t.Fatalf("g=%v c=%v t=%v", g, c, tm)
	}
}

func TestParseSoftnet(t *testing.T) {
	st := parseSoftnet("0001fe6e 00000002 00000001 00000000 00000000 00000000 00000000 00000000 00000000 00000003 00000000\n0000000a 00000001 00000000 00000000 00000000 00000000 00000000 00000000 00000000 00000001\n")
	if st.processed != 0x1fe6e+0xa || st.dropped != 3 || st.squeezed != 1 || st.receivedRPS != 4 {
		t.Fatalf("%+v", st)
	}
}

func TestParseConntrackStat(t *testing.T) {
	raw := "entries  new invalid ignore insert insert_failed drop early_drop icmp_error expect_new expect_create expect_delete search_restart\n" +
		"0000005c 00000001 00000002 00000000 00000003 00000004 00000005 00000000 00000000 00000000 00000000 00000000 00000006\n" +
		"0000005c 00000001 00000000 00000000 00000000 00000000 00000000 00000000 00000000 00000000 00000000 00000000 00000000\n"
	m := parseConntrackStat(raw)
	if m["new"] != 2 || m["invalid"] != 2 || m["insert_failed"] != 4 || m["search_restart"] != 6 {
		t.Fatalf("%v", m)
	}
	if _, ok := m["entries"]; ok {
		t.Fatal("entries should not be summed")
	}
}

func TestParseIPCTable(t *testing.T) {
	shm := "key shmid perms size cpid\n0 1 600 1024 2\n0 2 600 2048 3\n"
	n, size := parseIPCTable(shm, "size")
	if n != 2 || size != 3072 {
		t.Fatalf("n=%v size=%v", n, size)
	}
}

func TestParseMDStat(t *testing.T) {
	raw := "Personalities : [raid1]\nmd0 : active raid1 sda1[0] sdb1[1]\n      1000 blocks super 1.2 [2/2] [UU]\n\nmd1 : active raid1 sdc1[0] sdd1[1](F)\n      2000 blocks [2/1] [U_]\n      [=====>...............]  recovery = 33.0% (1/3)\n\nunused devices: <none>\n"
	arr := parseMDStat(raw)
	if len(arr) != 2 {
		t.Fatalf("%+v", arr)
	}
	if arr[0].Name != "md0" || arr[0].InUse != 2 || arr[0].Down != 0 || arr[0].Synced != 100 {
		t.Fatalf("md0 %+v", arr[0])
	}
	if arr[1].Name != "md1" || arr[1].Down != 1 || arr[1].Synced != 33 {
		t.Fatalf("md1 %+v", arr[1])
	}
}

func TestRatePerOp(t *testing.T) {
	if ratePerOp(200, 100, 10, 5) != 20 {
		t.Fatal("rate")
	}
	if ratePerOp(200, 100, 5, 5) != 0 {
		t.Fatal("zero ops")
	}
}

func TestIcmpChecksum(t *testing.T) {
	pkt := icmpEcho(1, 1, []byte("hi"))
	// checksum field must make the whole packet sum to 0xffff
	if icmpChecksum(pkt) != 0 {
		t.Fatalf("checksum residual %x", icmpChecksum(pkt))
	}
}

func TestParseSNMP6AndSockstat6(t *testing.T) {
	m := parseKVFloat("Ip6InReceives                    100\nIp6OutRequests                   40\nUdp6InDatagrams                  7\n")
	if m["Ip6InReceives"] != 100 || m["Udp6InDatagrams"] != 7 {
		t.Fatalf("%v", m)
	}
	st := parseSockstat6("TCP6: inuse 4 orphan 1\nUDP6: inuse 2\n")
	if st["tcp6_inuse"] != 4 || st["udp6_inuse"] != 2 {
		t.Fatalf("%v", st)
	}
}

func TestParseIPVS(t *testing.T) {
	raw := "   Total Incoming Outgoing         Incoming         Outgoing\n   Conns  Packets  Packets            Bytes            Bytes\n\n      0a      14      1e                64                c8\n"
	st, ok := parseIPVS(raw)
	if !ok || st.conns != 0xa || st.inPkts != 0x14 || st.outBytes != 0xc8 {
		t.Fatalf("%+v %v", st, ok)
	}
}

func TestParseRPCStats(t *testing.T) {
	raw := "net 0 0 0 0\nrpc 90 2 1\nio 100 200\nrc 5 1 0\nproc3 22 0 10 0 20 0 0 30 40 0 0 0 0 1 0 0 0 2 0 0 0 0 3\n"
	st := parseRPCStats(raw)
	if st["rpc_calls"] != 90 || st["proc3_getattr"] != 10 || st["proc3_lookup"] != 20 || st["proc3_read"] != 30 || st["io_write"] != 200 {
		t.Fatalf("%v", st)
	}
}

func TestParseKstatAndZramAndWireless(t *testing.T) {
	arc := parseKstat("13 1 0x01 1 1\nname type data\nhits 4 100\nmisses 4 25\nsize 4 1048576\nc 4 2097152\n")
	if arc["hits"] != 100 || arc["size"] != 1048576 {
		t.Fatalf("%v", arc)
	}
	z, ok := parseZramMMStat("1000 400 500 0 500")
	if !ok || z.orig != 1000 || z.compr != 400 || z.memUsed != 500 {
		t.Fatalf("%+v", z)
	}
	w := parseWireless("Inter-| sta-|   Quality        |   Discarded packets\n face | tus | link level noise |  nwid  crypt  frag  retry   misc\nwlan0: 0000   70.  -40.  -256        1      2     3      4      5\n")
	if len(w) != 1 || w[0].Name != "wlan0" || w[0].Link != 70 || w[0].Level != -40 || w[0].Retry != 4 {
		t.Fatalf("%+v", w)
	}
}

func TestProcM8Collect(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := dir + "/" + rel
		if i := strings.LastIndex(rel, "/"); i >= 0 {
			if err := os.MkdirAll(dir+"/"+rel[:i], 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("snmp6", "Ip6InReceives 10\nIp6OutRequests 4\nIp6OutForwDatagrams 0\nIp6InDelivers 9\n")
	write("sockstat6", "TCP6: inuse 3\nUDP6: inuse 1\n")
	write("ip_vs_stats", "   Total Incoming Outgoing         Incoming         Outgoing\n   Conns  Packets  Packets            Bytes            Bytes\n\n      01      02      03                04                05\n")
	write("nfs", "rpc 8 0 0\nproc3 22 0 1 0 2 0 0 3 4 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n")
	write("arcstats", "name type data\nhits 4 50\nmisses 4 10\nsize 4 4096\nc 4 8192\nc_min 4 1024\nc_max 4 16384\n")
	write("ksm/pages_shared", "2\n")
	write("ksm/pages_sharing", "4\n")
	write("ksm/pages_unshared", "1\n")
	write("ksm/pages_volatile", "1\n")
	write("block/zram0/mm_stat", "800 200 250\n")
	write("wireless", "Inter-| sta-|   Quality\n face | tus | link level noise |  nwid  crypt  frag  retry   misc\nwlan0: 0000   55.  -50.  -90         0      0     0      1      0\n")
	write("btrfs/aaaaaaaa/label", "pool\n")
	write("btrfs/aaaaaaaa/allocation/data/total_bytes", "1000\n")
	write("btrfs/aaaaaaaa/allocation/data/bytes_used", "400\n")
	write("btrfs/aaaaaaaa/allocation/metadata/total_bytes", "200\n")
	write("btrfs/aaaaaaaa/allocation/metadata/bytes_used", "50\n")
	write("btrfs/aaaaaaaa/allocation/system/total_bytes", "32\n")
	write("btrfs/aaaaaaaa/allocation/system/bytes_used", "8\n")

	p := &procCollector{}
	p.m8.snmp6 = dir + "/snmp6"
	p.m8.sockstat6 = dir + "/sockstat6"
	p.m8.ipvs = dir + "/ip_vs_stats"
	p.m8.nfs = dir + "/nfs"
	p.m8.arc = dir + "/arcstats"
	p.m8.ksm = dir + "/ksm"
	p.m8.zram = dir + "/block"
	p.m8.wireless = dir + "/wireless"
	p.m8.btrfs = dir + "/btrfs"
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	p.initM8(reg)
	if !p.m8.haveIPv6 || !p.m8.haveIPVS || !p.m8.haveZFS || !p.m8.haveKSM || !p.m8.haveZram || !p.m8.haveBtrfs {
		t.Fatalf("flags %+v", p.m8)
	}
	p.collectM8(reg, time.Now())
	if _, ok := reg.Chart("ipv6.packets"); !ok {
		t.Fatal("ipv6.packets missing")
	}
	if _, ok := reg.Chart("zfs.arc_size"); !ok {
		t.Fatal("zfs.arc_size missing")
	}
	if _, ok := reg.Chart("mem.zram_usage.zram0"); !ok {
		t.Fatal("zram chart missing")
	}
	if _, ok := reg.Chart("btrfs.data.pool"); !ok {
		t.Fatal("btrfs chart missing")
	}
}
