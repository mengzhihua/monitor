//go:build !linux

package collect

func readTimex() (state, unsync, offset float64, ok bool) {
	return 0, 0, 0, false
}
