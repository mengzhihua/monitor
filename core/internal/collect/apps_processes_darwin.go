//go:build darwin

package collect

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/sys/unix"
)

type appProcessMetadata struct {
	comm  [17]byte
	valid bool
}

func (p appProcess) readIdentity(ctx context.Context) (name, cmdline string, err error) {
	if !p.metadata.valid {
		return readPortableAppIdentity(ctx, p.process)
	}
	return p.metadata.readIdentity(ctx, p.process.CmdlineSliceWithContext)
}

func (m appProcessMetadata) readIdentity(ctx context.Context, args func(context.Context) ([]string, error)) (name, cmdline string, err error) {
	if err = ctx.Err(); err != nil {
		return "", "", err
	}
	name = unix.ByteSliceToString(m.comm[:])
	if name == "" {
		return "", "", nil
	}
	argv, argsErr := args(ctx)
	if err = ctx.Err(); err != nil {
		return "", "", err
	}
	// Match gopsutil's Darwin Name behavior for possibly truncated names:
	// expand argv[0], and keep an inaccessible long name unavailable rather
	// than assigning the process to a group using a truncated prefix.
	if len(name) >= 15 {
		if argsErr != nil {
			return "", "", argsErr
		}
		if len(argv) > 0 && argv[0] != "" {
			name = filepath.Base(argv[0])
		}
	}
	if argsErr == nil {
		cmdline = strings.Join(argv, " ")
	}
	return name, cmdline, nil
}

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
			metadata:  appProcessMetadata{comm: entry.P_comm, valid: true},
			process:   &process.Process{Pid: entry.P_pid},
			startedAt: entry.P_starttime.Sec*1e6 + int64(entry.P_starttime.Usec),
		})
	}
	return out, nil
}
