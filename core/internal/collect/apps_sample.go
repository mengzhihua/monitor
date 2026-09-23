package collect

import (
	"context"

	"github.com/shirou/gopsutil/v4/process"
)

// The portable path is also the fallback when a platform's combined reader
// cannot initialize. Failed CPU reads must not replace the cumulative baseline.
func readAppProcessPortable(ctx context.Context, p *process.Process, withIO bool) procCounters {
	times, err := p.TimesWithContext(ctx)
	if err != nil || times == nil {
		return procCounters{}
	}
	out := procCounters{cpuSec: times.User + times.System, ok: true}
	if memory, err := p.MemoryInfoWithContext(ctx); err == nil && memory != nil {
		out.rss = memory.RSS
	}
	if threads, err := p.NumThreadsWithContext(ctx); err == nil {
		out.threads = threads
	}
	if withIO {
		readAppProcessIO(ctx, p, &out)
	}
	return out
}

func readAppProcessIO(ctx context.Context, p *process.Process, out *procCounters) {
	if io, err := p.IOCountersWithContext(ctx); err == nil && io != nil {
		out.readB, out.writeB, out.hasIO = io.ReadBytes, io.WriteBytes, true
	}
}
