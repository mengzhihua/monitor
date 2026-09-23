//go:build !darwin

package collect

import (
	"context"

	"github.com/shirou/gopsutil/v4/process"
)

type appProcessMetadata struct{}

func (p appProcess) readIdentity(ctx context.Context) (name, cmdline string, err error) {
	return readPortableAppIdentity(ctx, p.process)
}

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
