//go:build !linux

package collect

import (
	"context"
	"fmt"
)

func openPerfHardware() (*perfHardware, error) {
	return nil, fmt.Errorf("perf: PerfEventOpen is linux-only")
}

type perfHardware struct{}

func (h *perfHardware) sample(ctx context.Context) (map[string]float64, error) {
	return nil, nil
}

func (h *perfHardware) close() {}
