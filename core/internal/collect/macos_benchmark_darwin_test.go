//go:build darwin

package collect

import (
	"context"
	"testing"
	"time"
)

// Includes all three real commands; the custom executable intentionally keeps
// the original full sysctl enumeration as a baseline on the same machine.
func BenchmarkMacosSample(b *testing.B) {
	for _, tc := range []struct{ name, command string }{
		{"enumeration", "/usr/sbin/sysctl"}, {"selected_keys", "sysctl"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			m := &macosCollector{cfg: macosConfig{Command: tc.command, Timeout: 3 * time.Second}}
			b.ReportAllocs()
			for b.Loop() {
				sample, err := m.sample(context.Background())
				if err != nil || sample.swapUsed == nil {
					b.Fatalf("real sample unavailable: %v", err)
				}
			}
		})
	}
}
