package collect

import (
	"context"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestParsePerfStat(t *testing.T) {
	raw := `12345,,cycles,1000000,100.00
678,,instructions:u,1000000,100.00
9,,cache-misses,1000000,100.00
`
	m := parsePerfStat(raw)
	if m["cycles"] != 12345 || m["instructions"] != 678 || m["cache_misses"] != 9 {
		t.Fatalf("%v", m)
	}
}

func TestPerfCollectorFixture(t *testing.T) {
	p := &perfCollector{}
	n := 0
	p.sample = func(context.Context) (map[string]float64, error) {
		n++
		base := float64(n) * 100
		return map[string]float64{
			"cycles": base, "instructions": base - 20, "cache_references": 10 * float64(n),
			"cache_misses": 2 * float64(n), "branches": 20 * float64(n), "branch_misses": float64(n),
		}, nil
	}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := p.Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	if err := p.Collect(context.Background(), reg, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("perf.cpu")
	if !ok {
		t.Fatal("missing perf.cpu")
	}
	_, vals := ch.LastValues()
	if vals["cycles"] != 100 || vals["instructions"] != 100 {
		t.Fatalf("%v", vals)
	}
}
