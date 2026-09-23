//go:build !windows

package collect

func pdhFill(perflibSnap) bool { return false }
