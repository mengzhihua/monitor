package collect

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

const typeperfCSVFixture = `" (PDH-CSV 4.0) (UTC)(0)","\\WINHOST\System\Processor Queue Length","\\WINHOST\Processor(_Total)\Interrupts/sec","\\WINHOST\Memory\Pool Paged Bytes","\\WINHOST\Memory\Pool Nonpaged Bytes","\\WINHOST\Memory\Pages Input/sec","\\WINHOST\Memory\Pages Output/sec","\\WINHOST\Memory\Page Faults/sec","\\WINHOST\Memory\Cache Bytes","\\WINHOST\Memory\Committed Bytes","\\WINHOST\Memory\Commit Limit","\\WINHOST\Objects\Mutexes","\\WINHOST\Objects\Semaphores","\\WINHOST\Objects\Events","\\WINHOST\LogicalDisk(C:)\Disk Read Bytes/sec","\\WINHOST\LogicalDisk(C:)\Disk Write Bytes/sec","\\WINHOST\LogicalDisk(C:)\Disk Reads/sec","\\WINHOST\LogicalDisk(C:)\Disk Writes/sec","\\WINHOST\LogicalDisk(C:)\% Free Space","\\WINHOST\PhysicalDisk(0 C:)\Disk Read Bytes/sec","\\WINHOST\PhysicalDisk(0 C:)\Disk Write Bytes/sec","\\WINHOST\PhysicalDisk(0 C:)\Disk Reads/sec","\\WINHOST\PhysicalDisk(0 C:)\Disk Writes/sec","\\WINHOST\PhysicalDisk(0 C:)\% Disk Time","\\WINHOST\Network Interface(eth0)\Bytes Received/sec","\\WINHOST\Network Interface(eth0)\Bytes Sent/sec","\\WINHOST\Network Interface(eth0)\Packets Received/sec","\\WINHOST\Network Interface(eth0)\Packets Sent/sec","\\WINHOST\Network Interface(eth0)\Packets Received Errors","\\WINHOST\Network Interface(eth0)\Packets Outbound Errors","\\WINHOST\Web Service(_Total)\Total Method Requests/sec","\\WINHOST\Web Service(_Total)\Current Connections","\\WINHOST\Web Service(_Total)\Bytes Received/sec","\\WINHOST\Web Service(_Total)\Bytes Sent/sec","\\WINHOST\Web Service(_Total)\Not Found Errors/sec","\\WINHOST\Web Service(_Total)\Locked Errors/sec","\\WINHOST\Web Service(_Total)\Current Anonymous Users","\\WINHOST\Web Service(_Total)\Current NonAnonymous Users","\\WINHOST\ASP.NET\Application Restarts","\\WINHOST\ASP.NET\Worker Process Restarts","\\WINHOST\ASP.NET\Requests Executing","\\WINHOST\ASP.NET\Requests In Application Queue","\\WINHOST\ASP.NET\Requests Failed","\\WINHOST\ASP.NET Applications(__Total__)\Sessions Active","\\WINHOST\ASP.NET Applications(__Total__)\Errors During Execution","\\WINHOST\.NET CLR Exceptions(_Global_)\# of Excep. Thrown / sec","\\WINHOST\.NET CLR LocksAndThreads(_Global_)\Queue Length / sec","\\WINHOST\.NET CLR LocksAndThreads(_Global_)\# of current physical Threads","\\WINHOST\.NET CLR LocksAndThreads(_Global_)\# of current recognized threads","\\WINHOST\.NET CLR LocksAndThreads(_Global_)\Contention Rate / sec","\\WINHOST\Hyper-V Hypervisor Virtual Processor(web01:Hv VP 0)\% Guest Run Time","\\WINHOST\Hyper-V Hypervisor Virtual Processor(web01:Hv VP 0)\% Hypervisor Run Time","\\WINHOST\SMB Server Shares(data)\Current Open File Count","\\WINHOST\SMB Server Shares(data)\Read Requests/sec","\\WINHOST\SMB Server Shares(data)\Write Requests/sec","\\WINHOST\Thermal Zone Information(\_TZ.TZ00)\Temperature","\\WINHOST\NUMA Node Memory(0)\Free & Zero Page List MBytes","\\WINHOST\NUMA Node Memory(0)\Standby List MBytes","\\WINHOST\NTDS\DS Directory Reads/sec","\\WINHOST\NTDS\DS Directory Writes/sec","\\WINHOST\NTDS\DS Directory Searches/sec","\\WINHOST\NTDS\LDAP Client Sessions","\\WINHOST\NTDS\LDAP Searches/sec","\\WINHOST\Certification Authority(CorpCA)\Requests/sec","\\WINHOST\Certification Authority(CorpCA)\Failed Requests/sec","\\WINHOST\AD FS\SSO Authentications/sec","\\WINHOST\AD FS\Extranet Account Lockouts/sec","\\WINHOST\MSExchangeTransport Queues(_total)\Active Mailbox Delivery Queue Length","\\WINHOST\MSExchangeTransport Queues(_total)\Retry Mailbox Delivery Queue Length","\\WINHOST\MSExchangeTransport Queues(_total)\Unreachable Queue Length","\\WINHOST\MSExchangeTransport Queues(_total)\Poison Queue Length","\\WINHOST\MSExchange RpcClientAccess\RPC Requests","\\WINHOST\Terminal Services\Active Sessions","\\WINHOST\Terminal Services\Inactive Sessions"
"09/22/2026 08:00:00.000","3","1200","104857600","20971520","10","4","80","52428800","2147483648","4294967296","400","50","800","4096","2048","5","2","42","8192","1024","8","3","11","1500","800","12","7","0","1","25","40","3000","2000","1","0","8","2","1","0","6","0","2","12","0","0.5","0","8","8","0.1","15","2","9","30","12","350.15","1024","256","40","10","20","15","30","2","0","5","0","3","1","0","0","4","2","1"
`

func TestSplitPerfPath(t *testing.T) {
	obj, inst, ctr := splitPerfPath(`\\WINHOST\Web Service(_Total)\Current Connections`)
	if obj != "web service" || inst != "_total" || ctr != "current connections" {
		t.Fatalf("got %q %q %q", obj, inst, ctr)
	}
	obj, inst, ctr = splitPerfPath(`Memory\Available Bytes`)
	if obj != "memory" || inst != "" || ctr != "available bytes" {
		t.Fatalf("singleton %q %q %q", obj, inst, ctr)
	}
	obj, inst, ctr = splitPerfPath(`\.NET CLR LocksAndThreads(_Global_)\Queue Length / sec`)
	if obj != ".net clr locksandthreads" || inst != "_global_" || !strings.Contains(ctr, "queue length") {
		t.Fatalf("clr %q %q %q", obj, inst, ctr)
	}
}

func TestParseTypeperfCSV(t *testing.T) {
	s := perflibSnap{}
	parseTypeperfCSV(s, []byte(typeperfCSVFixture))
	if v, ok := s.get("system", "", "processor queue length"); !ok || v != 3 {
		t.Fatalf("queue = %v %v", v, ok)
	}
	if v, ok := s.get("web service", "_total", "current connections"); !ok || v != 40 {
		t.Fatalf("iis conn = %v %v", v, ok)
	}
	if v, ok := s.get("logicaldisk", "c:", "disk reads/sec"); !ok || v != 5 {
		t.Fatalf("disk = %v %v", v, ok)
	}
	if v, ok := s.get("ntds", "", "ldap client sessions"); !ok || v != 15 {
		t.Fatalf("ad = %v %v", v, ok)
	}
}

func TestParsePerflibLines(t *testing.T) {
	s := perflibSnap{}
	parsePerflibLines(s, []byte("System\\Processor Queue Length=7\nMemory\\Pool Paged Bytes=100\n"))
	if v, ok := s.get("system", "", "processor queue length"); !ok || v != 7 {
		t.Fatalf("lines = %v %v", v, ok)
	}
}

func TestWindowsPerflibFixture(t *testing.T) {
	s := perflibSnap{}
	parseTypeperfCSV(s, []byte(typeperfCSVFixture))
	s[perflibKey("battery", "_total", "estimatedchargeremaining")] = 88
	c := &windowsCollector{
		cfg: windowsConfig{Command: "sc", Timeout: time.Second},
		snap: func(context.Context) (windowsSnap, error) {
			return windowsSnap{Running: 80, Blocked: 2, Total: 82, Threads: 400, Handles: 12000, Ctxt: 9000, hasCtxt: true}, nil
		},
		perflib: func(context.Context) (perflibSnap, error) { return s, nil },
		run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(scQueryFixture), nil
		},
	}
	reg := registry.New(&registry.Host{Hostname: "win", UpdateEvery: 1}, nil)
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
	want := []string{
		"system.cpu_queue",
		"system.intr",
		"mem.system_pool_size",
		"mem.swapio",
		"system.ipc_mutexes",
		"windows.services",
		"iis.website_requests_rate._total",
		"iis.website_errors_rate._total",
		"aspnet.requests_in_application_queue",
		"netframework.clr_exceptions._global_",
		"hyperv.vm_cpu." + sanitizeID("web01"),
		"smb.server_shares_current_open_file_count.data",
		"ad.directory_operations",
		"adcs.cert_requests.corpca",
		"adfs.sso_auth",
		"exchange.rpc_requests",
		"windows.terminal_services.sessions",
		"windows.power.charge",
		"windows.service_state.EventLog",
	}
	for _, id := range want {
		if _, ok := reg.Chart(id); !ok {
			t.Fatalf("missing %s", id)
		}
	}
	foundDisk, foundNet, foundThermal, foundNUMA := false, false, false, false
	for _, ch := range reg.Charts() {
		switch {
		case strings.HasPrefix(ch.ID, "windows.logical_disk.io."):
			foundDisk = true
		case strings.HasPrefix(ch.ID, "windows.net.traffic."):
			foundNet = true
		case strings.HasPrefix(ch.Context, "system.thermalzone_temperature"):
			foundThermal = true
		case strings.HasPrefix(ch.Context, "mem.numa_node_mem_usage"):
			foundNUMA = true
		}
	}
	if !foundDisk || !foundNet || !foundThermal || !foundNUMA {
		t.Fatalf("disk=%v net=%v thermal=%v numa=%v", foundDisk, foundNet, foundThermal, foundNUMA)
	}
	ch, _ := reg.Chart("system.cpu_queue")
	_, v := ch.LastValues()
	if v["load"] != 3 {
		t.Fatalf("cpu_queue = %v", v)
	}
	ch, _ = reg.Chart("iis.website_active_connections_count._total")
	_, v = ch.LastValues()
	if v["connections"] != 40 {
		t.Fatalf("iis connections = %v", v)
	}
	ch, _ = reg.Chart("windows.services")
	_, v = ch.LastValues()
	if v["running"] != 1 || v["stopped"] != 1 {
		t.Fatalf("services = %v", v)
	}
}

func TestThermalCelsius(t *testing.T) {
	if g := thermalCelsius(350.15); g < 76.9 || g > 77.1 {
		t.Fatalf("kelvin convert = %v", g)
	}
	if g := thermalCelsius(42); g != 42 {
		t.Fatalf("celsius passthrough = %v", g)
	}
}
