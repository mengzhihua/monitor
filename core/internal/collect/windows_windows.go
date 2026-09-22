//go:build windows

package collect

import (
	"context"

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
}

func wmiFillPerflib(s perflibSnap) {
	if s == nil {
		return
	}
	var sys []win32FmtSystem
	if err := wmi.Query("SELECT ProcessorQueueLength FROM Win32_PerfFormattedData_PerfOS_System", &sys); err == nil && len(sys) > 0 {
		s[perflibKey("system", "", "processor queue length")] = float64(sys[0].ProcessorQueueLength)
	}
	var mem []win32FmtMemory
	if err := wmi.Query("SELECT PoolPagedBytes,PoolNonpagedBytes,PagesInputPersec,PagesOutputPersec,PageFaultsPersec,CacheBytes,CommittedBytes,CommitLimit FROM Win32_PerfFormattedData_PerfOS_Memory", &mem); err == nil && len(mem) > 0 {
		m := mem[0]
		s[perflibKey("memory", "", "pool paged bytes")] = float64(m.PoolPagedBytes)
		s[perflibKey("memory", "", "pool nonpaged bytes")] = float64(m.PoolNonpagedBytes)
		s[perflibKey("memory", "", "pages input/sec")] = float64(m.PagesInputPersec)
		s[perflibKey("memory", "", "pages output/sec")] = float64(m.PagesOutputPersec)
		s[perflibKey("memory", "", "page faults/sec")] = float64(m.PageFaultsPersec)
		s[perflibKey("memory", "", "cache bytes")] = float64(m.CacheBytes)
		s[perflibKey("memory", "", "committed bytes")] = float64(m.CommittedBytes)
		s[perflibKey("memory", "", "commit limit")] = float64(m.CommitLimit)
	}
	var obj []win32FmtObjects
	if err := wmi.Query("SELECT Mutexes,Semaphores,Events FROM Win32_PerfFormattedData_PerfOS_Objects", &obj); err == nil && len(obj) > 0 {
		s[perflibKey("objects", "", "mutexes")] = float64(obj[0].Mutexes)
		s[perflibKey("objects", "", "semaphores")] = float64(obj[0].Semaphores)
		s[perflibKey("objects", "", "events")] = float64(obj[0].Events)
	}
	var bat []win32FmtBattery
	if err := wmi.Query("SELECT EstimatedChargeRemaining FROM Win32_Battery", &bat); err == nil && len(bat) > 0 {
		s[perflibKey("battery", "_total", "estimatedchargeremaining")] = float64(bat[0].EstimatedChargeRemaining)
	}
}
