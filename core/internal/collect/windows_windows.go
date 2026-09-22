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
