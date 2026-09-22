//go:build !windows

package collect

import "context"

func liveWindowsSnap(ctx context.Context) (windowsSnap, error) {
	return liveWindowsFromProcs(ctx)
}

func wmiFillPerflib(perflibSnap) {}
