//go:build windows

package collect

import (
	"context"
	"sync"

	"github.com/yusufpapurcu/wmi"
)

// win32PerfOSSystem is Win32_PerfRawData_PerfOS_System (cumulative counters).
type win32PerfOSSystem struct {
	ContextSwitchesPersec uint64
	Processes             uint32
	Threads               uint32
	SystemCallsPersec     uint64
}

func liveWindowsSnap(ctx context.Context) (windowsSnap, error) {
	if err := ctx.Err(); err != nil {
		return windowsSnap{}, err
	}
	s, err := liveWindowsFromProcs(ctx)
	if err != nil {
		return s, err
	}
	var dst []win32PerfOSSystem
	q := "SELECT ContextSwitchesPersec, Processes, Threads, SystemCallsPersec FROM Win32_PerfRawData_PerfOS_System"
	if err := wmi.Query(q, &dst); err == nil && len(dst) > 0 {
		s.Ctxt = dst[0].ContextSwitchesPersec
		s.hasCtxt = true
		if dst[0].Processes > 0 {
			s.Total = int64(dst[0].Processes)
			if s.Running+s.Blocked == 0 {
				s.Running = s.Total
			}
		}
		if dst[0].Threads > 0 {
			s.Threads = int64(dst[0].Threads)
		}
	}
	return s, nil
}

type win32FmtSystem struct {
	ProcessorQueueLength uint32
}

type win32FmtMemory struct {
	PoolPagedBytes    uint64
	PoolNonpagedBytes uint64
	PagesInputPersec  uint64
	PagesOutputPersec uint64
	PageFaultsPersec  uint64
	CacheBytes        uint64
	CommittedBytes    uint64
	CommitLimit       uint64
}

type win32FmtObjects struct {
	Mutexes    uint64
	Semaphores uint64
	Events     uint64
}

type win32FmtBattery struct {
	EstimatedChargeRemaining uint16
	DesignVoltage            uint32
}

type win32FmtProcessor struct {
	Name             string
	InterruptsPersec uint64
}

type win32FmtLogicalDisk struct {
	Name                 string
	DiskReadBytesPersec  uint64
	DiskWriteBytesPersec uint64
	DiskReadsPersec      uint64
	DiskWritesPersec     uint64
	PercentFreeSpace     uint64
}

type win32FmtPhysicalDisk struct {
	Name                 string
	DiskReadBytesPersec  uint64
	DiskWriteBytesPersec uint64
	DiskReadsPersec      uint64
	DiskWritesPersec     uint64
	PercentDiskTime      uint64
}

type win32FmtNet struct {
	Name                  string
	BytesReceivedPersec   uint64
	BytesSentPersec       uint64
	PacketsReceivedPersec uint64
	PacketsSentPersec     uint64
	PacketsReceivedErrors uint64
	PacketsOutboundErrors uint64
}

type win32FmtWebService struct {
	Name                      string
	TotalMethodRequestsPersec uint64
	GetRequestsPersec         uint64
	CurrentConnections        uint64
	BytesReceivedPersec       uint64
	BytesSentPersec           uint64
	NotFoundErrorsPersec      uint64
	LockedErrorsPersec        uint64
	CurrentAnonymousUsers     uint64
	CurrentNonAnonymousUsers  uint64
}

type win32FmtAppPool struct {
	Name                               string
	CurrentApplicationPoolState        uint32
	CurrentWorkerProcesses             uint32
	TotalWorkerProcessesCreated        uint64
	MaximumWorkerProcesses             uint32
	RecentWorkerProcessFailures        uint64
	TotalWorkerProcessFailures         uint64
	TotalWorkerProcessPingFailures     uint64
	TotalWorkerProcessStartupFailures  uint64
	TotalWorkerProcessShutdownFailures uint64
	TotalApplicationPoolRecycles       uint64
	CurrentApplicationPoolUptime       uint64
}

type win32FmtASPNET struct {
	ApplicationRestarts        uint64
	WorkerProcessRestarts      uint64
	RequestsExecuting          uint64
	RequestsInApplicationQueue uint64
	RequestsFailed             uint64
}

type win32FmtASPNETApp struct {
	Name                  string
	SessionsActive        uint64
	ErrorsDuringExecution uint64
}

type win32FmtCLRExcep struct {
	Name                      string
	NumberofExcepThrownPersec uint64
}

type win32FmtCLRLocks struct {
	Name                             string
	QueueLengthPersec                uint64
	NumberofcurrentphysicalThreads   uint64
	Numberofcurrentrecognizedthreads uint64
	ContentionRatePersec             uint64
}

type win32FmtHyperVVP struct {
	Name                     string
	PercentGuestRunTime      uint64
	PercentHypervisorRunTime uint64
}

type win32FmtSMBShare struct {
	Name                 string
	CurrentOpenFileCount uint64
	ReadRequestsPersec   uint64
	WriteRequestsPersec  uint64
}

type win32FmtThermalZone struct {
	Name        string
	Temperature uint64
}

type win32FmtNUMA struct {
	Name                      string
	FreeAndZeroPageListMBytes uint64
	StandbyListMBytes         uint64
}

type win32FmtNTDS struct {
	DSDirectoryReadsPersec    uint64
	DSDirectoryWritesPersec   uint64
	DSDirectorySearchesPersec uint64
	LDAPClientSessions        uint64
	LDAPSearchesPersec        uint64
}

type win32FmtCertAuth struct {
	Name                 string
	RequestsPersec       uint64
	FailedRequestsPersec uint64
}

type win32FmtADFS struct {
	SSOAuthenticationsPersec      uint64
	ExtranetAccountLockoutsPersec uint64
	FailedAuthenticationsPersec   uint64
}

type win32FmtExchangeQ struct {
	Name                             string
	ActiveMailboxDeliveryQueueLength uint64
	RetryMailboxDeliveryQueueLength  uint64
	UnreachableQueueLength           uint64
	PoisonQueueLength                uint64
}

type win32FmtExchangeRPC struct {
	RPCRequests uint64
}

type win32FmtTerminal struct {
	ActiveSessions   uint64
	InactiveSessions uint64
}

type win32TempProbe struct {
	Name           string
	CurrentReading int32
}

type msAcpiThermal struct {
	InstanceName       string
	CurrentTemperature uint32
}

// wmiClassSkip remembers WMI classes that failed (role not installed)
// so Collect does not re-query them every second.
var wmiClassSkip sync.Map

func wmiQueryClass(class, query string, dst any) bool {
	if v, ok := wmiClassSkip.Load(class); ok && v.(bool) {
		return false
	}
	if err := wmi.Query(query, dst); err != nil {
		wmiClassSkip.Store(class, true)
		return false
	}
	return true
}

func wmiPut(s perflibSnap, obj, inst, ctr string, v float64) {
	s[perflibKey(obj, inst, ctr)] = v
}

func wmiFillPerflib(s perflibSnap) {
	if s == nil {
		return
	}
	wmiFillCore(s)
	wmiFillDiskNet(s)
	wmiFillWeb(s)
	wmiFillRoles(s)
	wmiFillSensors(s)
}

func wmiFillCore(s perflibSnap) {
	var sys []win32FmtSystem
	if wmiQueryClass("PerfOS_System", "SELECT ProcessorQueueLength FROM Win32_PerfFormattedData_PerfOS_System", &sys) && len(sys) > 0 {
		wmiPut(s, "system", "", "processor queue length", float64(sys[0].ProcessorQueueLength))
	}
	var mem []win32FmtMemory
	if wmiQueryClass("PerfOS_Memory", "SELECT PoolPagedBytes,PoolNonpagedBytes,PagesInputPersec,PagesOutputPersec,PageFaultsPersec,CacheBytes,CommittedBytes,CommitLimit FROM Win32_PerfFormattedData_PerfOS_Memory", &mem) && len(mem) > 0 {
		m := mem[0]
		wmiPut(s, "memory", "", "pool paged bytes", float64(m.PoolPagedBytes))
		wmiPut(s, "memory", "", "pool nonpaged bytes", float64(m.PoolNonpagedBytes))
		wmiPut(s, "memory", "", "pages input/sec", float64(m.PagesInputPersec))
		wmiPut(s, "memory", "", "pages output/sec", float64(m.PagesOutputPersec))
		wmiPut(s, "memory", "", "page faults/sec", float64(m.PageFaultsPersec))
		wmiPut(s, "memory", "", "cache bytes", float64(m.CacheBytes))
		wmiPut(s, "memory", "", "committed bytes", float64(m.CommittedBytes))
		wmiPut(s, "memory", "", "commit limit", float64(m.CommitLimit))
	}
	var obj []win32FmtObjects
	if wmiQueryClass("PerfOS_Objects", "SELECT Mutexes,Semaphores,Events FROM Win32_PerfFormattedData_PerfOS_Objects", &obj) && len(obj) > 0 {
		wmiPut(s, "objects", "", "mutexes", float64(obj[0].Mutexes))
		wmiPut(s, "objects", "", "semaphores", float64(obj[0].Semaphores))
		wmiPut(s, "objects", "", "events", float64(obj[0].Events))
	}
	var cpu []win32FmtProcessor
	if wmiQueryClass("PerfOS_Processor", "SELECT Name,InterruptsPersec FROM Win32_PerfFormattedData_PerfOS_Processor", &cpu) {
		for _, p := range cpu {
			wmiPut(s, "processor", p.Name, "interrupts/sec", float64(p.InterruptsPersec))
		}
	}
	var bat []win32FmtBattery
	if wmiQueryClass("Win32_Battery", "SELECT EstimatedChargeRemaining,DesignVoltage FROM Win32_Battery", &bat) && len(bat) > 0 {
		wmiPut(s, "battery", "_total", "estimatedchargeremaining", float64(bat[0].EstimatedChargeRemaining))
		if bat[0].DesignVoltage > 0 {
			wmiPut(s, "battery", "_total", "designvoltage", float64(bat[0].DesignVoltage))
		}
	}
}

func wmiFillDiskNet(s perflibSnap) {
	var ld []win32FmtLogicalDisk
	if wmiQueryClass("PerfDisk_LogicalDisk", "SELECT Name,DiskReadBytesPersec,DiskWriteBytesPersec,DiskReadsPersec,DiskWritesPersec,PercentFreeSpace FROM Win32_PerfFormattedData_PerfDisk_LogicalDisk", &ld) {
		for _, d := range ld {
			wmiPut(s, "logicaldisk", d.Name, "disk read bytes/sec", float64(d.DiskReadBytesPersec))
			wmiPut(s, "logicaldisk", d.Name, "disk write bytes/sec", float64(d.DiskWriteBytesPersec))
			wmiPut(s, "logicaldisk", d.Name, "disk reads/sec", float64(d.DiskReadsPersec))
			wmiPut(s, "logicaldisk", d.Name, "disk writes/sec", float64(d.DiskWritesPersec))
			wmiPut(s, "logicaldisk", d.Name, "% free space", float64(d.PercentFreeSpace))
		}
	}
	var pd []win32FmtPhysicalDisk
	if wmiQueryClass("PerfDisk_PhysicalDisk", "SELECT Name,DiskReadBytesPersec,DiskWriteBytesPersec,DiskReadsPersec,DiskWritesPersec,PercentDiskTime FROM Win32_PerfFormattedData_PerfDisk_PhysicalDisk", &pd) {
		for _, d := range pd {
			wmiPut(s, "physicaldisk", d.Name, "disk read bytes/sec", float64(d.DiskReadBytesPersec))
			wmiPut(s, "physicaldisk", d.Name, "disk write bytes/sec", float64(d.DiskWriteBytesPersec))
			wmiPut(s, "physicaldisk", d.Name, "disk reads/sec", float64(d.DiskReadsPersec))
			wmiPut(s, "physicaldisk", d.Name, "disk writes/sec", float64(d.DiskWritesPersec))
			wmiPut(s, "physicaldisk", d.Name, "% disk time", float64(d.PercentDiskTime))
		}
	}
	var ni []win32FmtNet
	if wmiQueryClass("Tcpip_NetworkInterface", "SELECT Name,BytesReceivedPersec,BytesSentPersec,PacketsReceivedPersec,PacketsSentPersec,PacketsReceivedErrors,PacketsOutboundErrors FROM Win32_PerfFormattedData_Tcpip_NetworkInterface", &ni) {
		for _, n := range ni {
			wmiPut(s, "network interface", n.Name, "bytes received/sec", float64(n.BytesReceivedPersec))
			wmiPut(s, "network interface", n.Name, "bytes sent/sec", float64(n.BytesSentPersec))
			wmiPut(s, "network interface", n.Name, "packets received/sec", float64(n.PacketsReceivedPersec))
			wmiPut(s, "network interface", n.Name, "packets sent/sec", float64(n.PacketsSentPersec))
			wmiPut(s, "network interface", n.Name, "packets received errors", float64(n.PacketsReceivedErrors))
			wmiPut(s, "network interface", n.Name, "packets outbound errors", float64(n.PacketsOutboundErrors))
		}
	}
}

func wmiFillWeb(s perflibSnap) {
	var ws []win32FmtWebService
	if wmiQueryClass("W3SVC_WebService", "SELECT Name,TotalMethodRequestsPersec,GetRequestsPersec,CurrentConnections,BytesReceivedPersec,BytesSentPersec,NotFoundErrorsPersec,LockedErrorsPersec,CurrentAnonymousUsers,CurrentNonAnonymousUsers FROM Win32_PerfFormattedData_W3SVC_WebService", &ws) {
		for _, w := range ws {
			wmiPut(s, "web service", w.Name, "total method requests/sec", float64(w.TotalMethodRequestsPersec))
			wmiPut(s, "web service", w.Name, "get requests/sec", float64(w.GetRequestsPersec))
			wmiPut(s, "web service", w.Name, "current connections", float64(w.CurrentConnections))
			wmiPut(s, "web service", w.Name, "bytes received/sec", float64(w.BytesReceivedPersec))
			wmiPut(s, "web service", w.Name, "bytes sent/sec", float64(w.BytesSentPersec))
			wmiPut(s, "web service", w.Name, "not found errors/sec", float64(w.NotFoundErrorsPersec))
			wmiPut(s, "web service", w.Name, "locked errors/sec", float64(w.LockedErrorsPersec))
			wmiPut(s, "web service", w.Name, "current anonymous users", float64(w.CurrentAnonymousUsers))
			wmiPut(s, "web service", w.Name, "current nonanonymous users", float64(w.CurrentNonAnonymousUsers))
		}
	}
	var pools []win32FmtAppPool
	if wmiQueryClass("APPPOOLWAS", "SELECT Name,CurrentApplicationPoolState,CurrentWorkerProcesses,TotalWorkerProcessesCreated,MaximumWorkerProcesses,RecentWorkerProcessFailures,TotalWorkerProcessFailures,TotalWorkerProcessPingFailures,TotalWorkerProcessStartupFailures,TotalWorkerProcessShutdownFailures,TotalApplicationPoolRecycles,CurrentApplicationPoolUptime FROM Win32_PerfFormattedData_APPPOOLWAS_APPPOOLWAS", &pools) {
		for _, p := range pools {
			wmiPut(s, "app_pool_was", p.Name, "current application pool state", float64(p.CurrentApplicationPoolState))
			wmiPut(s, "app_pool_was", p.Name, "current worker processes", float64(p.CurrentWorkerProcesses))
			wmiPut(s, "app_pool_was", p.Name, "total worker processes created", float64(p.TotalWorkerProcessesCreated))
			wmiPut(s, "app_pool_was", p.Name, "maximum worker processes", float64(p.MaximumWorkerProcesses))
			wmiPut(s, "app_pool_was", p.Name, "recent worker process failures", float64(p.RecentWorkerProcessFailures))
			wmiPut(s, "app_pool_was", p.Name, "total worker process failures", float64(p.TotalWorkerProcessFailures))
			wmiPut(s, "app_pool_was", p.Name, "total worker process ping failures", float64(p.TotalWorkerProcessPingFailures))
			wmiPut(s, "app_pool_was", p.Name, "total worker process startup failures", float64(p.TotalWorkerProcessStartupFailures))
			wmiPut(s, "app_pool_was", p.Name, "total worker process shutdown failures", float64(p.TotalWorkerProcessShutdownFailures))
			wmiPut(s, "app_pool_was", p.Name, "total application pool recycles", float64(p.TotalApplicationPoolRecycles))
			wmiPut(s, "app_pool_was", p.Name, "current application pool uptime", float64(p.CurrentApplicationPoolUptime))
		}
	}
	var asp []win32FmtASPNET
	if wmiQueryClass("ASPNET", "SELECT ApplicationRestarts,WorkerProcessRestarts,RequestsExecuting,RequestsInApplicationQueue,RequestsFailed FROM Win32_PerfFormattedData_ASPNET_ASPNET", &asp) && len(asp) > 0 {
		a := asp[0]
		wmiPut(s, "asp.net", "", "application restarts", float64(a.ApplicationRestarts))
		wmiPut(s, "asp.net", "", "worker process restarts", float64(a.WorkerProcessRestarts))
		wmiPut(s, "asp.net", "", "requests executing", float64(a.RequestsExecuting))
		wmiPut(s, "asp.net", "", "requests in application queue", float64(a.RequestsInApplicationQueue))
		wmiPut(s, "asp.net", "", "requests failed", float64(a.RequestsFailed))
	}
	var aspa []win32FmtASPNETApp
	if wmiQueryClass("ASPNETApplications", "SELECT Name,SessionsActive,ErrorsDuringExecution FROM Win32_PerfFormattedData_ASPNET_ASPNETApplications", &aspa) {
		for _, a := range aspa {
			wmiPut(s, "asp.net applications", a.Name, "sessions active", float64(a.SessionsActive))
			wmiPut(s, "asp.net applications", a.Name, "errors during execution", float64(a.ErrorsDuringExecution))
		}
	}
	var clr []win32FmtCLRExcep
	if wmiQueryClass("NETCLRExceptions", "SELECT Name,NumberofExcepThrownPersec FROM Win32_PerfFormattedData_NETFramework_NETCLRExceptions", &clr) {
		for _, c := range clr {
			wmiPut(s, ".net clr exceptions", c.Name, "# of excep. thrown / sec", float64(c.NumberofExcepThrownPersec))
		}
	}
	var locks []win32FmtCLRLocks
	if wmiQueryClass("NETCLRLocks", "SELECT Name,QueueLengthPersec,NumberofcurrentphysicalThreads,Numberofcurrentrecognizedthreads,ContentionRatePersec FROM Win32_PerfFormattedData_NETFramework_NETCLRLocksAndThreads", &locks) {
		for _, c := range locks {
			wmiPut(s, ".net clr locksandthreads", c.Name, "queue length / sec", float64(c.QueueLengthPersec))
			wmiPut(s, ".net clr locksandthreads", c.Name, "# of current physical threads", float64(c.NumberofcurrentphysicalThreads))
			wmiPut(s, ".net clr locksandthreads", c.Name, "# of current recognized threads", float64(c.Numberofcurrentrecognizedthreads))
			wmiPut(s, ".net clr locksandthreads", c.Name, "contention rate / sec", float64(c.ContentionRatePersec))
		}
	}
}

func wmiFillRoles(s perflibSnap) {
	var hv []win32FmtHyperVVP
	if wmiQueryClass("HyperVVirtualProcessor", "SELECT Name,PercentGuestRunTime,PercentHypervisorRunTime FROM Win32_PerfFormattedData_HvStats_HyperVHypervisorVirtualProcessor", &hv) {
		for _, v := range hv {
			wmiPut(s, "hyper-v hypervisor virtual processor", v.Name, "% guest run time", float64(v.PercentGuestRunTime))
			wmiPut(s, "hyper-v hypervisor virtual processor", v.Name, "% hypervisor run time", float64(v.PercentHypervisorRunTime))
		}
	}
	var smb []win32FmtSMBShare
	if wmiQueryClass("SMBServerShares", "SELECT Name,CurrentOpenFileCount,ReadRequestsPersec,WriteRequestsPersec FROM Win32_PerfFormattedData_SMBServerShares_SMBServerShares", &smb) {
		for _, v := range smb {
			wmiPut(s, "smb server shares", v.Name, "current open file count", float64(v.CurrentOpenFileCount))
			wmiPut(s, "smb server shares", v.Name, "read requests/sec", float64(v.ReadRequestsPersec))
			wmiPut(s, "smb server shares", v.Name, "write requests/sec", float64(v.WriteRequestsPersec))
		}
	}
	var tz []win32FmtThermalZone
	if wmiQueryClass("ThermalZoneInformation", "SELECT Name,Temperature FROM Win32_PerfFormattedData_Counters_ThermalZoneInformation", &tz) {
		for _, v := range tz {
			wmiPut(s, "thermal zone information", v.Name, "temperature", float64(v.Temperature))
		}
	}
	var numa []win32FmtNUMA
	if wmiQueryClass("NUMANodeMemory", "SELECT Name,FreeAndZeroPageListMBytes,StandbyListMBytes FROM Win32_PerfFormattedData_PerfOS_NUMANodeMemory", &numa) {
		for _, v := range numa {
			wmiPut(s, "numa node memory", v.Name, "free & zero page list mbytes", float64(v.FreeAndZeroPageListMBytes))
			wmiPut(s, "numa node memory", v.Name, "standby list mbytes", float64(v.StandbyListMBytes))
		}
	}
	var ntds []win32FmtNTDS
	if wmiQueryClass("NTDS", "SELECT DSDirectoryReadsPersec,DSDirectoryWritesPersec,DSDirectorySearchesPersec,LDAPClientSessions,LDAPSearchesPersec FROM Win32_PerfFormattedData_NTDS_NTDS", &ntds) && len(ntds) > 0 {
		v := ntds[0]
		wmiPut(s, "ntds", "", "ds directory reads/sec", float64(v.DSDirectoryReadsPersec))
		wmiPut(s, "ntds", "", "ds directory writes/sec", float64(v.DSDirectoryWritesPersec))
		wmiPut(s, "ntds", "", "ds directory searches/sec", float64(v.DSDirectorySearchesPersec))
		wmiPut(s, "ntds", "", "ldap client sessions", float64(v.LDAPClientSessions))
		wmiPut(s, "ntds", "", "ldap searches/sec", float64(v.LDAPSearchesPersec))
	}
	var ca []win32FmtCertAuth
	if wmiQueryClass("CertificationAuthority", "SELECT Name,RequestsPersec,FailedRequestsPersec FROM Win32_PerfFormattedData_CertSvc_CertificationAuthority", &ca) {
		for _, v := range ca {
			wmiPut(s, "certification authority", v.Name, "requests/sec", float64(v.RequestsPersec))
			wmiPut(s, "certification authority", v.Name, "failed requests/sec", float64(v.FailedRequestsPersec))
		}
	}
	var adfs []win32FmtADFS
	if wmiQueryClass("ADFS", "SELECT SSOAuthenticationsPersec,ExtranetAccountLockoutsPersec,FailedAuthenticationsPersec FROM Win32_PerfFormattedData_ADFS_ADFS", &adfs) && len(adfs) > 0 {
		v := adfs[0]
		wmiPut(s, "ad fs", "", "sso authentications/sec", float64(v.SSOAuthenticationsPersec))
		wmiPut(s, "ad fs", "", "extranet account lockouts/sec", float64(v.ExtranetAccountLockoutsPersec))
		wmiPut(s, "ad fs", "", "failed authentications/sec", float64(v.FailedAuthenticationsPersec))
	}
	var eq []win32FmtExchangeQ
	if wmiQueryClass("MSExchangeTransportQueues", "SELECT Name,ActiveMailboxDeliveryQueueLength,RetryMailboxDeliveryQueueLength,UnreachableQueueLength,PoisonQueueLength FROM Win32_PerfFormattedData_MSExchangeTransportQueues_MSExchangeTransportQueues", &eq) {
		for _, v := range eq {
			wmiPut(s, "msexchangetransport queues", v.Name, "active mailbox delivery queue length", float64(v.ActiveMailboxDeliveryQueueLength))
			wmiPut(s, "msexchangetransport queues", v.Name, "retry mailbox delivery queue length", float64(v.RetryMailboxDeliveryQueueLength))
			wmiPut(s, "msexchangetransport queues", v.Name, "unreachable queue length", float64(v.UnreachableQueueLength))
			wmiPut(s, "msexchangetransport queues", v.Name, "poison queue length", float64(v.PoisonQueueLength))
		}
	}
	var rpc []win32FmtExchangeRPC
	if wmiQueryClass("MSExchangeRpcClientAccess", "SELECT RPCRequests FROM Win32_PerfFormattedData_MSExchangeRpcClientAccess_MSExchangeRpcClientAccess", &rpc) && len(rpc) > 0 {
		wmiPut(s, "msexchange rpcclientaccess", "", "rpc requests", float64(rpc[0].RPCRequests))
	}
	var ts []win32FmtTerminal
	if wmiQueryClass("TerminalServices", "SELECT ActiveSessions,InactiveSessions FROM Win32_PerfFormattedData_LocalSessionManager_TerminalServices", &ts) && len(ts) > 0 {
		wmiPut(s, "terminal services", "", "active sessions", float64(ts[0].ActiveSessions))
		wmiPut(s, "terminal services", "", "inactive sessions", float64(ts[0].InactiveSessions))
	} else if wmiQueryClass("TermService", "SELECT ActiveSessions,InactiveSessions FROM Win32_PerfFormattedData_TermService_TerminalServices", &ts) && len(ts) > 0 {
		wmiPut(s, "terminal services", "", "active sessions", float64(ts[0].ActiveSessions))
		wmiPut(s, "terminal services", "", "inactive sessions", float64(ts[0].InactiveSessions))
	}
}

func wmiFillSensors(s perflibSnap) {
	var probes []win32TempProbe
	if wmiQueryClass("Win32_TemperatureProbe", "SELECT Name,CurrentReading FROM Win32_TemperatureProbe", &probes) {
		for _, p := range probes {
			if p.CurrentReading == 0 {
				continue
			}
			wmiPut(s, "win32_temperatureprobe", p.Name, "currentreading", float64(p.CurrentReading))
		}
	}
	if v, ok := wmiClassSkip.Load("MSAcpi_ThermalZoneTemperature"); ok && v.(bool) {
		return
	}
	var acpi []msAcpiThermal
	if err := wmi.QueryNamespace("SELECT InstanceName,CurrentTemperature FROM MSAcpi_ThermalZoneTemperature", &acpi, `root\wmi`); err != nil {
		wmiClassSkip.Store("MSAcpi_ThermalZoneTemperature", true)
		return
	}
	for _, z := range acpi {
		if z.CurrentTemperature == 0 {
			continue
		}
		wmiPut(s, "msacpi_thermalzonetemperature", z.InstanceName, "currenttemperature", float64(z.CurrentTemperature))
	}
}
