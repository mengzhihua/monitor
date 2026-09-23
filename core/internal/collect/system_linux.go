//go:build linux

package collect

import (
	"bytes"
	"io"
	"os"

	"github.com/shirou/gopsutil/v4/cpu"
)

// linuxTimes reads per-CPU lines from /proc/stat and stops before the intr
// line. gopsutil materializes that whole file, and the intr line is one
// counter per IRQ.
func (c *cpuCollector) linuxTimes() ([]cpu.TimesStat, bool) {
	b, err := readProcStatCPU(&c.scratch)
	if err != nil || len(b) == 0 {
		return nil, false
	}
	c.times = parseCPUStat(b, c.times[:0])
	if len(c.times) == 0 {
		return nil, false
	}
	return c.times, true
}

func readProcStatCPU(buf *[]byte) ([]byte, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b := (*buf)[:0]
	var tmp [512]byte
	for {
		n, err := f.Read(tmp[:])
		if n > 0 {
			b = append(b, tmp[:n]...)
			if end, ok := cpuPrefixEnd(b); ok {
				b = b[:end]
				*buf = b
				return b, nil
			}
		}
		if err == io.EOF {
			*buf = b
			return b, nil
		}
		if err != nil {
			*buf = b
			return nil, err
		}
		if n == 0 {
			*buf = b
			return b, nil
		}
	}
}

// cpuPrefixEnd is the index of the first line that is not a cpu times line,
// once that line has started. The caller drops it.
func cpuPrefixEnd(b []byte) (int, bool) {
	i := 0
	for i < len(b) {
		nl := bytes.IndexByte(b[i:], '\n')
		if nl < 0 {
			return 0, false
		}
		line := b[i : i+nl]
		next := i + nl + 1
		if len(line) == 0 || !bytes.HasPrefix(line, []byte("cpu")) {
			return i, true
		}
		i = next
	}
	return 0, false
}

func parseCPUStat(b []byte, dst []cpu.TimesStat) []cpu.TimesStat {
	rest := b
	for len(rest) > 0 {
		line := rest
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			line = rest[:i]
			rest = rest[i+1:]
		} else {
			rest = nil
		}
		if !bytes.HasPrefix(line, []byte("cpu")) {
			break
		}
		f := bytes.Fields(line)
		if len(f) < 5 {
			continue
		}
		// The aggregate "cpu" row is the sum of cpuN. Keep it only when it is
		// the only row, matching a host that does not publish per-CPU lines.
		if bytes.Equal(f[0], []byte("cpu")) && hasCPULine(rest) {
			continue
		}
		var t cpu.TimesStat
		t.User = statTicks(field(f, 1))
		t.Nice = statTicks(field(f, 2))
		t.System = statTicks(field(f, 3))
		t.Idle = statTicks(field(f, 4))
		t.Iowait = statTicks(field(f, 5))
		t.Irq = statTicks(field(f, 6))
		t.Softirq = statTicks(field(f, 7))
		t.Steal = statTicks(field(f, 8))
		t.Guest = statTicks(field(f, 9))
		t.GuestNice = statTicks(field(f, 10))
		dst = append(dst, t)
	}
	return dst
}

func hasCPULine(b []byte) bool {
	for len(b) > 0 {
		line := b
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			line = b[:i]
			b = b[i+1:]
		} else {
			b = nil
		}
		if bytes.HasPrefix(line, []byte("cpu")) {
			return true
		}
		if len(bytes.TrimSpace(line)) > 0 {
			return false
		}
	}
	return false
}

func field(f [][]byte, i int) []byte {
	if i >= len(f) {
		return nil
	}
	return f[i]
}

func statTicks(b []byte) float64 {
	var n uint64
	for _, c := range b {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + uint64(c-'0')
	}
	return float64(n) / float64(clkTicks)
}
