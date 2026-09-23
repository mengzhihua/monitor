//go:build darwin

package collect

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/sys/unix"
)

type appProcessMetadata struct {
	ppid  int32
	uid   uint32
	gid   uint32
	comm  [17]byte
	valid bool
}

func (p appProcess) readOwners(ctx context.Context, a *appsCollector, st *pidState) {
	if ctx.Err() != nil {
		return
	}
	if !p.metadata.valid {
		a.readPortableOwners(ctx, p.process, st)
		return
	}
	st.ppid = p.metadata.ppid
	st.user = a.lookupUserID(strconv.FormatUint(uint64(p.metadata.uid), 10), "")
	st.osGroup = a.lookupOSGroup(p.metadata.gid)
}

func appProcessFromKinfo(entry *unix.KinfoProc) appProcess {
	return appProcess{
		metadata: appProcessMetadata{
			comm: entry.Proc.P_comm, valid: true, ppid: entry.Eproc.Ppid,
			// Match gopsutil: Username uses the effective UID, while the
			// first Gids entry is the real GID, not Ucred.Groups[0].
			uid: entry.Eproc.Ucred.Uid, gid: entry.Eproc.Pcred.P_rgid,
		},
		process:   &process.Process{Pid: entry.Proc.P_pid},
		startedAt: entry.Proc.P_starttime.Sec*1e6 + int64(entry.Proc.P_starttime.Usec),
	}
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
		entry := &entries[i]
		if entry.Proc.P_pid <= 0 {
			continue // gopsutil's existence check also excludes kernel PID 0
		}
		out = append(out, appProcessFromKinfo(entry))
	}
	return out, nil
}
