//go:build linux

package collect

import (
	"bytes"
	"os"
	"strconv"
)

// clkTicks is Linux USER_HZ. /proc stat times are always in these ticks,
// independent of the kernel CONFIG_HZ.
const clkTicks = 100

var pageSize = uint64(os.Getpagesize())

func init() {
	if pageSize == 0 {
		pageSize = 4096
	}
}

// readProcCounters reads CPU, RSS, threads and disk bytes from procfs.
// One stat/statm/io pass replaces four gopsutil lookups per process.
func readProcCounters(pid int32) (cpuSec float64, rss uint64, threads int32, readB, writeB uint64, hasIO, ok bool) {
	id := strconv.FormatInt(int64(pid), 10)
	stat, err := os.ReadFile("/proc/" + id + "/stat")
	if err != nil {
		return 0, 0, 0, 0, 0, false, false
	}
	ut, st, thr, okStat := parseProcPIDStat(stat)
	if !okStat {
		return 0, 0, 0, 0, 0, false, false
	}
	cpuSec = float64(ut+st) / clkTicks
	threads = thr
	if sm, err := os.ReadFile("/proc/" + id + "/statm"); err == nil {
		f := bytes.Fields(sm)
		if len(f) >= 2 {
			if pages, err := strconv.ParseUint(string(f[1]), 10, 64); err == nil {
				rss = pages * pageSize
			}
		}
	}
	if raw, err := os.ReadFile("/proc/" + id + "/io"); err == nil {
		readB, writeB, hasIO = parseProcIO(raw)
	}
	return cpuSec, rss, threads, readB, writeB, hasIO, true
}

// parseProcPIDStat reads utime, stime and num_threads. comm may contain spaces
// and parentheses, so fields are taken after the last ')'.
func parseProcPIDStat(b []byte) (utime, stime uint64, threads int32, ok bool) {
	i := bytes.LastIndexByte(b, ')')
	if i < 0 || i+2 >= len(b) {
		return 0, 0, 0, false
	}
	f := bytes.Fields(b[i+2:])
	// utime is field 14, stime 15, num_threads 20; index 0 here is field 3.
	if len(f) < 18 {
		return 0, 0, 0, false
	}
	var err error
	if utime, err = strconv.ParseUint(string(f[11]), 10, 64); err != nil {
		return 0, 0, 0, false
	}
	if stime, err = strconv.ParseUint(string(f[12]), 10, 64); err != nil {
		return 0, 0, 0, false
	}
	thr, err := strconv.ParseInt(string(f[17]), 10, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return utime, stime, int32(thr), true
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
