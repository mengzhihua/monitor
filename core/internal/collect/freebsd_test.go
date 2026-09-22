package collect

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

const freebsdSysctlFixture = `
hw.pagesize=4096
vm.stats.sys.v_swtch=10000
vm.stats.sys.v_intr=2000
vm.stats.sys.v_soft=300
vm.stats.vm.v_forks=80
vm.stats.vm.v_wire_count=1024
vm.stats.vm.v_laundry_count=64
kern.ipc.semmni=50
kern.ipc.semmns=340
kern.ipc.semusz=10
kern.ipc.semaem=25
kern.ipc.shmmni=120
kern.ipc.shm_nused=4
kern.ipc.shmmax=67108864
kern.ipc.msgmni=40
kern.ipc.msgtql=12
dev.cpu.0.temperature=45.1C
dev.cpu.1.temperature=47.0C
`

func TestParseSysctl(t *testing.T) {
	mixed := []byte("kern.ipc.semmni: 50\nvm.stats.sys.v_swtch=100\n# comment\n")
	m := parseSysctl(mixed)
	if m["kern.ipc.semmni"] != "50" || m["vm.stats.sys.v_swtch"] != "100" {
		t.Fatalf("%v", m)
	}
}

func TestFreeBSDCollectorFixture(t *testing.T) {
	m := parseSysctl([]byte(freebsdSysctlFixture))
	c := &freebsdCollector{
		cfg:  freebsdConfig{Command: "sysctl", Timeout: time.Second},
		read: func(context.Context) (map[string]string, error) { return m, nil },
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
		"system.ctxt", "system.intr", "system.softirq", "system.forks",
		"mem.wired", "mem.laundry", "system.ipc_semaphores",
		"system.ipc_semaphore_values", "system.ipc_shared_mem_segs",
		"system.ipc_shared_mem_size", "system.ipc_msq_queues",
		"system.ipc_msq_messages", "freebsd.cpu.temperature",
	} {
		ch, ok := reg.Chart(id)
		if !ok {
			t.Fatalf("missing chart %s", id)
		}
		_, v := ch.LastValues()
		if len(v) == 0 {
			t.Fatalf("%s empty values", id)
		}
	}
	ch, _ := reg.Chart("freebsd.cpu.temperature")
	_, v := ch.LastValues()
	if v["cpu0"] != 45.1 || v["cpu1"] != 47 || v["hottest"] != 47 {
		t.Fatalf("temps = %v", v)
	}
	wired, _ := reg.Chart("mem.wired")
	_, wv := wired.LastValues()
	if wv["wired"] != 4 {
		t.Fatalf("wired = %v (want 4 MiB)", wv)
	}
}

func TestFreeBSDAutoDisable(t *testing.T) {
	if runtime.GOOS == "freebsd" {
		t.Skip("live freebsd")
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := (&freebsdCollector{}).Init(reg); err == nil {
		t.Fatal("expected disable off freebsd")
	}
}

func TestFreeBSDRegistered(t *testing.T) {
	found := false
	for _, n := range Available() {
		if n == "freebsd" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("freebsd collector not registered")
	}
}
