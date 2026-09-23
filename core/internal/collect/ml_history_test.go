package collect

import (
	"fmt"
	"math/rand"
	"reflect"
	"sync"
	"testing"
)

// Preserve the old query as a benchmark reference; correctness tests below
// compare against a separate plain slice rather than another ring traversal.
func scanMLRates(m *mlCollector, chart, dim string, after, before int64) []float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.dims[chart+"|"+dim]
	if st == nil || st.bitN == 0 || len(st.bits) == 0 {
		return nil
	}
	start := 0
	if st.bitN == len(st.bits) {
		start = st.bitI
	}
	var out []float64
	for k := 0; k < st.bitN; k++ {
		bit := st.bits[(start+k)%len(st.bits)]
		if bit.ts >= after && bit.ts <= before {
			out = append(out, bit.rate)
		}
	}
	return out
}

func TestMLHistoryMatchesInsertionOrder(t *testing.T) {
	for _, capacity := range []int{1, 2, 3, 17, 120} {
		for _, ordered := range []bool{true, false} {
			t.Run(fmt.Sprintf("capacity=%d/ordered=%t", capacity, ordered), func(t *testing.T) {
				st := &dimML{}
				m := &mlCollector{dims: map[string]*dimML{"chart|dim": st}}
				var history []anomBit
				rng := rand.New(rand.NewSource(9))
				for i := 0; i < capacity*3+10; i++ {
					ts := int64(i / 2) // duplicates must retain both samples at an inclusive boundary
					if !ordered && i%7 == 0 {
						ts -= int64(rng.Intn(20))
					}
					bit := anomBit{ts: ts, rate: float64((i % 2) * 100)}
					st.pushBit(bit, capacity)
					history = append(history, bit)
					if len(history) > capacity {
						history = history[1:]
					}
					for _, window := range [][2]int64{{ts, ts}, {ts - 3, ts + 2}, {-100, 10000}, {10000, 11000}, {ts + 1, ts - 1}, {0, 0}} {
						var want []float64
						for _, b := range history {
							if b.ts >= window[0] && b.ts <= window[1] {
								want = append(want, b.rate)
							}
						}
						got := m.RatesBetween("chart", "dim", window[0], window[1])
						if !reflect.DeepEqual(got, want) {
							t.Fatalf("sample=%d window=%v got=%v want=%v", i, window, got, want)
						}
						if len(got) > 0 {
							got[0] = -1
						}
					}
				}
			})
		}
	}
}

func TestMLHistoryOrderRecoversAndResize(t *testing.T) {
	st := &dimML{}
	m := &mlCollector{dims: map[string]*dimML{"chart|dim": st}}
	if got := m.RatesBetween("missing", "dim", 0, 100); got != nil {
		t.Fatal("missing history")
	}
	if got := m.RatesBetween("chart", "dim", 0, 100); got != nil {
		t.Fatal("empty history")
	}
	for _, ts := range []int64{10, 5, 8} {
		st.pushBit(anomBit{ts: ts, rate: 100}, 3)
	}
	if st.bitDisorder == 0 {
		t.Fatal("out of order history must use scan")
	}
	st.pushBit(anomBit{ts: 9, rate: 0}, 3)
	if st.bitDisorder != 0 {
		t.Fatal("ordered suffix did not restore binary search")
	}
	if got := m.RatesBetween("chart", "dim", 8, 9); !reflect.DeepEqual(got, []float64{100, 0}) {
		t.Fatalf("recovered query: %v", got)
	}
	st.pushBit(anomBit{ts: 1, rate: 0}, 3)
	st.pushBit(anomBit{ts: 2, rate: 100}, 2)
	if st.bitDisorder != 0 || st.bitN != 1 {
		t.Fatal("resize retained old order state")
	}
	if got := m.RatesBetween("chart", "dim", 0, 100); !reflect.DeepEqual(got, []float64{100}) {
		t.Fatalf("resized query: %v", got)
	}
}

func TestMLHistoryConcurrentReadAndWrite(t *testing.T) {
	st := &dimML{}
	m := &mlCollector{dims: map[string]*dimML{"chart|dim": st}}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			m.mu.Lock()
			st.pushBit(anomBit{ts: int64(i), rate: float64(i)}, 120)
			m.mu.Unlock()
		}
	}()
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				rates := m.RatesBetween("chart", "dim", 100, 900)
				for j, v := range rates {
					if v < 100 || v > 900 || (j > 0 && v != rates[j-1]+1) {
						t.Error("inconsistent snapshot")
						return
					}
				}
			}
		}()
	}
	wg.Wait()
}

var mlHistoryBenchmarkResult []float64

func BenchmarkMLHistoryRange(b *testing.B) {
	for _, capacity := range []int{120, 14400} {
		for _, ordered := range []bool{true, false} {
			st := &dimML{}
			for i := 0; i < capacity*2; i++ {
				ts := int64(i)
				if !ordered && i%7 == 0 {
					ts -= 20
				}
				st.pushBit(anomBit{ts: ts, rate: float64((i % 2) * 100)}, capacity)
			}
			m := &mlCollector{dims: map[string]*dimML{"chart|dim": st}}
			for _, window := range []struct {
				name          string
				after, before int64
			}{
				{"narrow", int64(capacity*2 - 3), int64(capacity * 2)},
				{"empty", 0, int64(capacity - 30)},
				{"full", -100, int64(capacity * 2)},
			} {
				for _, mode := range []string{"scan", "search"} {
					b.Run(fmt.Sprintf("capacity=%d/ordered=%t/%s/%s", capacity, ordered, window.name, mode), func(b *testing.B) {
						b.ReportAllocs()
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							if mode == "scan" {
								mlHistoryBenchmarkResult = scanMLRates(m, "chart", "dim", window.after, window.before)
							} else {
								mlHistoryBenchmarkResult = m.RatesBetween("chart", "dim", window.after, window.before)
							}
						}
					})
				}
			}
		}
	}
}

func originalMLPushBit(st *dimML, bit anomBit, capacity int) {
	if capacity < 1 {
		return
	}
	if len(st.bits) != capacity {
		st.bits = make([]anomBit, capacity)
		st.bitI, st.bitN = 0, 0
	}
	st.bits[st.bitI] = bit
	st.bitI++
	if st.bitI == len(st.bits) {
		st.bitI = 0
	}
	if st.bitN < len(st.bits) {
		st.bitN++
	}
}

func BenchmarkMLHistoryAppend(b *testing.B) {
	for _, mode := range []string{"original", "track_order"} {
		b.Run(mode, func(b *testing.B) {
			st := &dimML{}
			for i := 0; i < 120; i++ {
				st.pushBit(anomBit{ts: int64(i)}, 120)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				bit := anomBit{ts: int64(i) + 120, rate: 100}
				if mode == "original" {
					originalMLPushBit(st, bit, 120)
				} else {
					st.pushBit(bit, 120)
				}
			}
		})
	}
}
