//go:build linux

package collect

import "golang.org/x/sys/unix"

const staUnsync = 0x0040

func readTimex() (state, unsync, offset float64, ok bool) {
	var tx unix.Timex
	st, err := unix.Adjtimex(&tx)
	if err != nil {
		return 0, 0, 0, false
	}
	unsync = 0
	if tx.Status&staUnsync != 0 {
		unsync = 1
	}
	return float64(st), unsync, float64(tx.Offset), true
}
