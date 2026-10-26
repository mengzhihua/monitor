// Package sliceutil holds small slice helpers shared by long-lived buffers.
package sliceutil

// KeepTail returns the last keep elements in a new slice. A reslice of the
// original array would stay live for the whole capacity, including every
// string in the discarded prefix.
func KeepTail[T any](s []T, keep int) []T {
	if keep < 0 {
		keep = 0
	}
	if len(s) <= keep {
		return s
	}
	out := make([]T, keep)
	copy(out, s[len(s)-keep:])
	return out
}
