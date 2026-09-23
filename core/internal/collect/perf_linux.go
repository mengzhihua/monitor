//go:build linux

package collect

import (
	"context"
	"unsafe"

	"golang.org/x/sys/unix"
)

type perfHardware struct {
	fds   []int
	names []string
}

func openPerfHardware() (*perfHardware, error) {
	events := []struct {
		name   string
		config uint64
	}{
		{"cycles", unix.PERF_COUNT_HW_CPU_CYCLES},
		{"instructions", unix.PERF_COUNT_HW_INSTRUCTIONS},
		{"cache_references", unix.PERF_COUNT_HW_CACHE_REFERENCES},
		{"cache_misses", unix.PERF_COUNT_HW_CACHE_MISSES},
		{"branches", unix.PERF_COUNT_HW_BRANCH_INSTRUCTIONS},
		{"branch_misses", unix.PERF_COUNT_HW_BRANCH_MISSES},
	}
	h := &perfHardware{}
	var first error
	for _, ev := range events {
		attr := unix.PerfEventAttr{
			Type:   unix.PERF_TYPE_HARDWARE,
			Size:   uint32(unsafe.Sizeof(unix.PerfEventAttr{})),
			Config: ev.config,
			Bits:   unix.PerfBitDisabled,
		}
		fd, err := unix.PerfEventOpen(&attr, -1, 0, -1, 0)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.PERF_EVENT_IOC_ENABLE, 0)
		if errno != 0 {
			unix.Close(fd)
			if first == nil {
				first = errno
			}
			continue
		}
		h.fds = append(h.fds, fd)
		h.names = append(h.names, ev.name)
	}
	if len(h.fds) == 0 {
		if first == nil {
			first = unix.EPERM
		}
		return nil, first
	}
	return h, nil
}

func (h *perfHardware) sample(context.Context) (map[string]float64, error) {
	out := map[string]float64{}
	buf := make([]byte, 8)
	for i, fd := range h.fds {
		n, err := unix.Read(fd, buf)
		if err != nil || n < 8 {
			continue
		}
		out[h.names[i]] = float64(uint64(buf[0]) | uint64(buf[1])<<8 | uint64(buf[2])<<16 | uint64(buf[3])<<24 |
			uint64(buf[4])<<32 | uint64(buf[5])<<40 | uint64(buf[6])<<48 | uint64(buf[7])<<56)
	}
	if len(out) == 0 {
		return nil, unix.EIO
	}
	return out, nil
}

func (h *perfHardware) close() {
	if h == nil {
		return
	}
	for _, fd := range h.fds {
		unix.Close(fd)
	}
	h.fds = nil
}
