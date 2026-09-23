//go:build !linux

package collect

import "github.com/shirou/gopsutil/v4/cpu"

func (c *cpuCollector) linuxTimes() ([]cpu.TimesStat, bool) { return nil, false }
