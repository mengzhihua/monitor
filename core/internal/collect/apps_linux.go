//go:build linux

package collect

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strconv"
)

// clkTicks is Linux USER_HZ. /proc stat times are always in these ticks,
// independent of the kernel CONFIG_HZ.
const clkTicks = 100

// pfKThread is PF_KTHREAD in linux/sched.h. Kernel threads have no useful
// /proc/<pid>/io, and they are most of the tasks on a quiet host.
const pfKThread = 0x00200000

var pageSize = uint64(os.Getpagesize())

func init() {
	if pageSize == 0 {
		pageSize = 4096
	}
}

// listProcPIDs reads numeric /proc entries. gopsutil's process list stats
// every pid and reads /proc/<pid>/stat just to build the slice.
func listProcPIDs(dst []int32) ([]int32, bool) {
	f, err := os.Open("/proc")
	if err != nil {
		return dst[:0], false
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return dst[:0], false
	}
	dst = dst[:0]
	for _, n := range names {
		if n == "" || n[0] < '1' || n[0] > '9' {
			continue
		}
		pid, err := strconv.ParseInt(n, 10, 32)
		if err != nil {
			continue
		}
		dst = append(dst, int32(pid))
	}
	return dst, true
}

// readProcSample reads CPU, RSS, threads and (unless skipped) disk bytes
// from one stat file plus an optional io file.
func readProcSample(pid int32, skipIO bool, buf *[]byte) procCounters {
	id := strconv.FormatInt(int64(pid), 10)
	b, err := readFileReuse("/proc/"+id+"/stat", buf)
	if err != nil {
		return procCounters{}
	}
	name, ppid, ut, st, rssPages, threads, kthread, ok := parseProcPIDStat(b)
	if !ok {
		return procCounters{}
	}
	out := procCounters{
		name:    name,
		ppid:    ppid,
		cpuSec:  float64(ut+st) / float64(clkTicks),
		rss:     rssPages * pageSize,
		threads: threads,
		kthread: kthread,
		ok:      true,
	}
	if skipIO || kthread {
		if skipIO {
			out.ioDenied = true
		}
		return out
	}
	ib, err := readFileReuse("/proc/"+id+"/io", buf)
	if err != nil {
		if errors.Is(err, os.ErrPermission) || errors.Is(err, os.ErrNotExist) {
			out.ioDenied = true
		}
		return out
	}
	out.readB, out.writeB, out.hasIO = parseProcIO(ib)
	return out
}

func readProcCmdline(pid int32) string {
	b, err := os.ReadFile("/proc/" + strconv.FormatInt(int64(pid), 10) + "/cmdline")
	if err != nil || len(b) == 0 {
		return ""
	}
	for len(b) > 0 && b[len(b)-1] == 0 {
		b = b[:len(b)-1]
	}
	for i, c := range b {
		if c == 0 {
			b[i] = ' '
		}
	}
	return string(b)
}

func readProcOwners(pid int32) (uid, gid uint32, haveUID, haveGID bool) {
	b, err := os.ReadFile("/proc/" + strconv.FormatInt(int64(pid), 10) + "/status")
	if err != nil {
		return 0, 0, false, false
	}
	for _, line := range bytes.Split(b, []byte{'\n'}) {
		key, rest, found := bytes.Cut(line, []byte{':'})
		if !found {
			continue
		}
		fields := bytes.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		n, err := strconv.ParseUint(string(fields[0]), 10, 32)
		if err != nil {
			continue
		}
		switch string(key) {
		case "Uid":
			uid, haveUID = uint32(n), true
		case "Gid":
			gid, haveGID = uint32(n), true
		}
		if haveUID && haveGID {
			return uid, gid, true, true
		}
	}
	return uid, gid, haveUID, haveGID
}

func readFileReuse(path string, buf *[]byte) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b := (*buf)[:0]
	var tmp [1024]byte
	for {
		n, err := f.Read(tmp[:])
		if n > 0 {
			b = append(b, tmp[:n]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			*buf = b
			return nil, err
		}
		if n == 0 {
			break
		}
	}
	*buf = b
	return b, nil
}

// parseProcPIDStat reads comm, ppid, utime, stime, rss (pages) and num_threads.
// comm may contain spaces and parentheses, so fields are taken after the last ')'.
func parseProcPIDStat(b []byte) (name string, ppid int32, utime, stime, rssPages uint64, threads int32, kthread, ok bool) {
	open := bytes.IndexByte(b, '(')
	close := bytes.LastIndexByte(b, ')')
	if open < 0 || close < open || close+2 >= len(b) {
		return "", 0, 0, 0, 0, 0, false, false
	}
	name = string(b[open+1 : close])
	f := bytes.Fields(b[close+2:])
	// utime is field 14, stime 15, num_threads 20, rss 24; index 0 here is field 3.
	if len(f) < 18 {
		return "", 0, 0, 0, 0, 0, false, false
	}
	if v, err := strconv.ParseInt(string(f[1]), 10, 32); err == nil {
		ppid = int32(v)
	}
	if flags, err := strconv.ParseUint(string(f[6]), 10, 64); err == nil {
		kthread = flags&pfKThread != 0
	}
	var err error
	if utime, err = strconv.ParseUint(string(f[11]), 10, 64); err != nil {
		return "", 0, 0, 0, 0, 0, false, false
	}
	if stime, err = strconv.ParseUint(string(f[12]), 10, 64); err != nil {
		return "", 0, 0, 0, 0, 0, false, false
	}
	thr, err := strconv.ParseInt(string(f[17]), 10, 32)
	if err != nil {
		return "", 0, 0, 0, 0, 0, false, false
	}
	if len(f) > 21 {
		rssPages, _ = strconv.ParseUint(string(f[21]), 10, 64)
	}
	return name, ppid, utime, stime, rssPages, int32(thr), kthread, true
}

func parseProcIO(b []byte) (readB, writeB uint64, ok bool) {
	for _, line := range bytes.Split(b, []byte{'\n'}) {
		key, val, found := bytes.Cut(line, []byte{':'})
		if !found {
			continue
		}
		n, err := strconv.ParseUint(string(bytes.TrimSpace(val)), 10, 64)
		if err != nil {
			continue
		}
		switch string(key) {
		case "read_bytes":
			readB, ok = n, true
		case "write_bytes":
			writeB, ok = n, true
		}
	}
	return readB, writeB, ok
}
