//go:build linux

package collect

// agentCPU reads the agent process from /proc/self/stat. gopsutil's process
// CPU path opens several files per tick; utime and stime are already in this one.
func (p *profileCollector) agentCPU() (user, sys float64, ok bool) {
	b, err := readInto("/proc/self/stat", &p.scratch)
	if err != nil {
		return 0, 0, false
	}
	_, _, _, ut, st, _, _, _, ok := parseProcPIDStat(b)
	if !ok {
		return 0, 0, false
	}
	return float64(ut) / float64(clkTicks), float64(st) / float64(clkTicks), true
}
