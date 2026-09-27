package api

import (
	"fmt"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// Keep the disk set fixed while unrelated collectors grow. The operations
// overview only needs these four charts for each node.
func BenchmarkOperationsDiskMetrics(b *testing.B) {
	for _, charts := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("charts=%d/disks=4", charts), func(b *testing.B) {
			reg := registry.New(&registry.Host{ID: "local", UpdateEvery: 1}, nil)
			now := time.Unix(1700000000, 0)
			for i := range charts {
				reg.AddChart(&registry.Chart{ID: fmt.Sprintf("fixture.%05d", i), Context: "fixture.other", Priority: i % 13})
			}
			for i := range 4 {
				id := fmt.Sprintf("disk_space.%d", i)
				reg.AddChart(&registry.Chart{ID: id, Context: "disk.space", Family: fmt.Sprintf("/mount/%d", i), Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "avail"}}})
				if err := reg.Collect(id, now, map[string]float64{"used": float64(20 + i), "avail": float64(80 - i)}); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if len(diskMetricsFor(reg, "live", now.Unix())) != 4 {
					b.Fatal("disk selection changed")
				}
			}
		})
	}
}
