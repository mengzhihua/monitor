//go:build darwin

package collect

import (
	"context"

	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/sys/unix"
)

// gopsutil's Darwin ProcessesWithContext first reads this same table, then
// probes existence and start time separately for every PID. The table already
// provides both, so keep its process identity and avoid those per-PID syscalls.
func listAppProcesses(ctx context.Context) ([]appProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	out := make([]appProcess, 0, len(entries))
	for i := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entry := &entries[i].Proc
		if entry.P_pid <= 0 {
			continue // gopsutil's existence check also excludes kernel PID 0
		}
		out = append(out, appProcess{
			process:   &process.Process{Pid: entry.P_pid},
			startedAt: entry.P_starttime.Sec*1e6 + int64(entry.P_starttime.Usec),
		})
	}
	return out, nil
}
