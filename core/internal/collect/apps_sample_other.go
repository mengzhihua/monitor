//go:build !darwin

package collect

import (
	"context"

	"github.com/shirou/gopsutil/v4/process"
)

func readAppProcessSample(ctx context.Context, p *process.Process, withIO bool) procCounters {
	return readAppProcessPortable(ctx, p, withIO)
}
