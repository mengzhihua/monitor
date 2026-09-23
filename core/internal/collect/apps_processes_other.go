//go:build !darwin

package collect

import (
	"context"

	"github.com/shirou/gopsutil/v4/process"
)

func listAppProcesses(ctx context.Context) ([]appProcess, error) {
	processes, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]appProcess, 0, len(processes))
	for _, p := range processes {
		out = append(out, appProcess{process: p})
	}
	return out, nil
}
