package collect

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

const freebsdM22Sysctl = freebsdSysctlFixture + `
vm.stats.sys.v_syscall=9000
vm.stats.vm.v_free_count=2048
vm.stats.vm.v_active_count=4096
vm.stats.vm.v_inactive_count=512
vm.stats.vm.v_cache_count=128
vm.stats.vm.v_vm_faults=1000
vm.stats.vm.v_io_faults=40
vm.stats.vm.v_cow_faults=20
vm.stats.vm.v_cow_optim=5
vm.stats.vm.v_intrans=2
vm.stats.vm.v_swappgsin=80
vm.stats.vm.v_swappgsout=10
dev.cpu.0.freq=2400
hw.intrnames=irq0 cpu0:timer uart0
hw.intrcnt=10 20 3
net.isr.dispatched=100
net.isr.hybrid_dispatched=4
net.isr.qdrops=1
net.isr.queued=8
net.inet.tcp.states=0 1 2 3 17 0 0 0 0 0 0
net.inet.tcp.stats.tcps_rcvtotal=500
net.inet.tcp.stats.tcps_sndtotal=400
net.inet.tcp.stats.tcps_rcvbadsum=2
net.inet.tcp.stats.tcps_sndrexmitpack=7
net.inet.tcp.stats.tcps_rcvbadoff=1
net.inet.tcp.stats.tcps_rcvshort=0
net.inet.tcp.stats.tcps_drops=3
net.inet.tcp.stats.tcps_connattempt=9
net.inet.tcp.stats.tcps_accepts=6
net.inet.tcp.stats.tcps_conndrops=1
net.inet.udp.stats.udps_ipackets=50
net.inet.udp.stats.udps_opackets=40
net.inet.udp.stats.udps_noport=2
net.inet.udp.stats.udps_badsum=1
net.inet.icmp.stats.icps_received=8
net.inet.icmp.stats.icps_sent=4
net.inet.icmp.stats.icps_error=1
net.inet.ip.stats.ips_total=900
net.inet.ip.stats.ips_localout=800
net.inet.ip.stats.ips_forward=10
net.inet.ip.stats.ips_delivered=850
net.inet.ip.stats.ips_odropped=2
net.inet.ip.stats.ips_badvers=1
net.inet.ip.stats.ips_cantforward=3
net.inet6.ip6.stats.ip6s_total=30
net.inet6.ip6.stats.ip6s_localout=20
net.inet6.ip6.stats.ip6s_delivered=25
kstat.zfs.misc.arcstats.size=104857600
kstat.zfs.misc.arcstats.c=94371840
kstat.zfs.misc.arcstats.c_min=33554432
kstat.zfs.misc.arcstats.c_max=2147483648
kstat.zfs.misc.arcstats.hits=800
kstat.zfs.misc.arcstats.misses=200
kstat.zfs.misc.arcstats.l2_hits=10
kstat.zfs.misc.arcstats.l2_misses=4
kstat.zfs.misc.arcstats.l2_size=2097152
kstat.zfs.misc.arcstats.l2_asize=1048576
kstat.zfs.misc.arcstats.l2_read_bytes=4096
kstat.zfs.misc.arcstats.l2_write_bytes=2048
kstat.zfs.misc.arcstats.memory_throttle_count=2
kstat.zfs.misc.arcstats.evict_skip=1
kstat.zfs.misc.arcstats.deleted=3
kstat.zfs.misc.arcstats.mutex_miss=0
kstat.zfs.misc.arcstats.hash_collisions=5
kstat.zfs.misc.arcstats.mru_size=41943040
kstat.zfs.misc.arcstats.mfu_size=62914560
kstat.zfs.misc.arcstats.demand_data_hits=100
kstat.zfs.misc.arcstats.demand_data_misses=10
kstat.zfs.misc.arcstats.prefetch_data_hits=20
kstat.zfs.misc.arcstats.prefetch_data_misses=5
kstat.zfs.misc.zio_trim.bytes=8192
kstat.zfs.misc.zio_trim.success=4
kstat.zfs.misc.zio_trim.failed=1
kstat.zfs.misc.zio_trim.unsupported=0
net.inet.ip.fw.dyn_count=2
`

const freebsdIPFWFixture = `
00100    12     1234 allow ip from any to any
00200     3      400 deny ip from 10.0.0.0/8 to any
00200     1      100 deny ip from 192.168.0.0/16 to any
65535     0        0 deny ip from any to any
`

const freebsdGstatFixture = `
ada0 40960 81920 10 20 0 5
`

const freebsdDFFixture = `
Filesystem 1024-blocks Used Available Capacity Mounted on
zroot/ROOT/default 2048000 1024000 1024000 50% /
`

const freebsdNetstatFixture = `
Name    Mtu Network       Address              Ipkts Ierrs Idrop     Ibytes    Opkts Oerrs     Obytes  Coll
em0    1500 <Link#1>      00:0c:29:aa:bb:cc     1000     1     2     123456      800     3     234567     4
em0    1500 192.168.1.0/24 192.168.1.10           900     -     -          -      700     -          -     -
`

func TestParseIPFWList(t *testing.T) {
	rules := parseIPFWList(freebsdIPFWFixture)
	if len(rules) != 4 {
		t.Fatalf("rules=%d %#v", len(rules), rules)
	}
	if rules[0].ID != "100" || rules[0].Packets != 12 || rules[0].Bytes != 1234 {
		t.Fatalf("rule0=%+v", rules[0])
	}
	if rules[2].ID != "200_1" {
		t.Fatalf("duplicate rule id=%s", rules[2].ID)
	}
}

func TestParseGstatFixture(t *testing.T) {
	rows := parseGstat(freebsdGstatFixture)
	if len(rows) != 1 || rows[0].Name != "ada0" || rows[0].ReadBytes != 40960 {
		t.Fatalf("%+v", rows)
	}
}

func TestParseGstatHeader(t *testing.T) {
	raw := `
dT: 1.001s  w: 1.000s
 L(q)  ops/s    r/s   kBps   ms/r    w/s   kBps   ms/w   %busy Name
    0     12      4     32   0.40      8     64   0.80    2.1 ada0
`
	rows := parseGstat(raw)
	if len(rows) != 1 || rows[0].Name != "ada0" {
		t.Fatalf("%+v", rows)
	}
	if rows[0].Reads != 4 || rows[0].Writes != 8 || rows[0].ReadBytes != 32*1024 || rows[0].Busy != 2.1 {
		t.Fatalf("mapped=%+v", rows[0])
	}
}

func TestParseDFAndNetstat(t *testing.T) {
	df := parseDF(freebsdDFFixture)
	if len(df) != 1 || df[0].Mount != "/" || df[0].UsedKB != 1024000 {
		t.Fatalf("df=%+v", df)
	}
	ifs := parseNetstatIBN(freebsdNetstatFixture)
	if len(ifs) != 1 || ifs[0].Name != "em0" || ifs[0].Ibytes != 123456 || ifs[0].Coll != 4 {
		t.Fatalf("if=%+v", ifs)
	}
}

func TestFreeBSDM22Fixture(t *testing.T) {
	m := parseSysctl([]byte(freebsdM22Sysctl))
	c := &freebsdCollector{
		cfg:        freebsdConfig{Command: "sysctl", Timeout: time.Second},
		read:       func(context.Context) (map[string]string, error) { return m, nil },
		ipfwList:   []byte(freebsdIPFWFixture),
		gstatOut:   []byte(freebsdGstatFixture),
		dfOut:      []byte(freebsdDFFixture),
		netstatOut: []byte(freebsdNetstatFixture),
	}
	reg := registry.New(&registry.Host{Hostname: "fbsd", UpdateEvery: 1}, nil)
	if err := c.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := c.Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	if err := c.Collect(context.Background(), reg, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		"system.syscalls", "mem.pgfaults", "mem.swapio", "system.ram", "mem.available",
		"cpu.scaling_cur_freq", "system.interrupts", "system.softnet_stat",
		"ipv4.tcpsock", "ipv4.tcppackets", "ipv4.tcperrors", "ipv4.tcphandshake",
		"ipv4.udppackets", "ipv4.packets", "ipv6.packets",
		"zfs.arc_size", "zfs.hits_rate", "zfs.l2_size", "zfs.memory_ops", "zfs.trim_bytes", "zfs.trim_requests",
		"ipfw.packets", "ipfw.bytes", "ipfw.mem",
		"disk.ada0", "disk_ops.ada0", "system.io",
		"disk_space.root_d3b073", // may hash; check prefix below
		"net.em0", "system.net",
	} {
		if _, ok := reg.Chart(id); ok {
			continue
		}
		if id == "disk_space.root_d3b073" {
			found := false
			for _, ch := range reg.Charts() {
				if len(ch.ID) >= 11 && ch.ID[:11] == "disk_space." {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("missing disk_space.*")
			}
			continue
		}
		t.Fatalf("missing chart %s", id)
	}
	tcp, _ := reg.Chart("ipv4.tcpsock")
	_, tv := tcp.LastValues()
	if tv["CurrEstab"] != 17 {
		t.Fatalf("tcpsock=%v", tv)
	}
	arc, _ := reg.Chart("zfs.arc_size")
	_, av := arc.LastValues()
	if av["arcsz"] != 100 { // 104857600 / 1MiB
		t.Fatalf("arc=%v", av)
	}
	ipfw, _ := reg.Chart("ipfw.packets")
	_, pv := ipfw.LastValues()
	if pv["100"] == 0 && pv["00100"] == 0 {
		// incremental: second tick with same counters → 0 rate. First tick skipped.
		// After two identical dumps, rate is 0. Check dimension exists instead.
		found := false
		for _, d := range ipfw.Dims() {
			if d.ID == "100" {
				found = true
			}
		}
		if !found {
			t.Fatalf("ipfw dims=%v values=%v", ipfw.Dims(), pv)
		}
	}
	mempg, _ := reg.Chart("mem.pgfaults")
	_, mv := mempg.LastValues()
	if mv["memory"] == 0 && mv["io_requiring"] == 0 {
		found := false
		for _, d := range mempg.Dims() {
			if d.ID == "memory" {
				found = true
			}
		}
		if !found {
			t.Fatalf("pgfaults dims missing memory: %v", mv)
		}
	}
}

func TestSysctlNums(t *testing.T) {
	n := sysctlNums("{ 0 1 2 3 17 }")
	if len(n) != 5 || n[4] != 17 {
		t.Fatalf("%v", n)
	}
}

func TestFreeBSDCrossCompile(t *testing.T) {
	if runtime.GOOS == "freebsd" {
		t.Skip("already freebsd")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "collect.test")
	cmd := exec.Command("go", "test", "-c", "-o", out, ".")
	cmd.Env = append(os.Environ(), "GOOS=freebsd", "GOARCH=amd64", "CGO_ENABLED=0")
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("freebsd cross-compile: %v\n%s", err, b)
	}
}
