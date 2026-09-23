//go:build !linux

package collect

func readProcCounters(int32) (cpuSec float64, rss uint64, threads int32, readB, writeB uint64, hasIO, ok bool) {
	return 0, 0, 0, 0, 0, false, false
}
