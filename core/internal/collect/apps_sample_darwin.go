//go:build darwin

package collect

import (
	"context"
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/shirou/gopsutil/v4/process"
)

const (
	appProcPIDTaskInfo = 4 // PROC_PIDTASKINFO from the macOS SDK's sys/proc_info.h
	appTaskInfoSize    = int32(unsafe.Sizeof(process.ProcTaskInfo{}))
)

type appDarwinTimebase struct{ numer, denom uint32 }

type appDarwinTaskReader struct {
	info           func(int32, int32, uint64, *process.ProcTaskInfo, int32) int32
	secondsPerTick float64
}

var appDarwinTaskAPI = sync.OnceValues(loadAppDarwinTaskReader)

func loadAppDarwinTaskReader() (reader *appDarwinTaskReader, err error) {
	library, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_LAZY|purego.RTLD_LOCAL)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = purego.Dlclose(library)
		}
	}()
	infoAddress, err := purego.Dlsym(library, "proc_pidinfo")
	if err != nil {
		return nil, err
	}
	clockAddress, err := purego.Dlsym(library, "mach_timebase_info")
	if err != nil {
		return nil, err
	}
	reader = &appDarwinTaskReader{}
	purego.RegisterFunc(&reader.info, infoAddress)
	var clock func(*appDarwinTimebase) int32
	purego.RegisterFunc(&clock, clockAddress)
	var timebase appDarwinTimebase
	if status := clock(&timebase); status != 0 || timebase.numer == 0 || timebase.denom == 0 {
		return nil, fmt.Errorf("mach_timebase_info unavailable (status %d)", status)
	}
	reader.secondsPerTick = float64(timebase.numer) / float64(timebase.denom) / 1e9
	// Keep the library loaded for this reader's process-long lifetime. Resolving
	// three symbols and reopening the library for each metric dwarfs the call.
	return reader, nil
}

func (r *appDarwinTaskReader) read(pid int32) procCounters {
	if pid <= 0 {
		return procCounters{}
	}
	var info process.ProcTaskInfo
	if n := r.info(pid, appProcPIDTaskInfo, 0, &info, appTaskInfoSize); n != appTaskInfoSize {
		// Exit, denied access, and partial reads are unavailable, not a real
		// zero CPU counter. Keep the last valid CPU/IO baseline in the collector.
		return procCounters{}
	}
	return procCounters{
		cpuSec:  (float64(info.Total_user) + float64(info.Total_system)) * r.secondsPerTick,
		rss:     info.Resident_size,
		threads: info.Threadnum,
		ok:      true,
	}
}

func readAppProcessSample(ctx context.Context, p *process.Process, withIO bool) procCounters {
	if ctx.Err() != nil {
		return procCounters{}
	}
	reader, err := appDarwinTaskAPI()
	if err != nil {
		return readAppProcessPortable(ctx, p, withIO)
	}
	out := reader.read(p.Pid)
	if out.ok && withIO {
		readAppProcessIO(ctx, p, &out)
	}
	return out
}
