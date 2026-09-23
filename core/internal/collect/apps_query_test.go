package collect

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"
)

// Preserve the previous full-copy/full-sort implementation as an independent
// result oracle and benchmark, including the API timestamp and total count.
func referenceProcessTable(a *appsCollector, args map[string]string) Table {
	a.tableMu.RLock()
	rows := append([]ProcessRow(nil), a.published...)
	var collectedAt int64
	if !a.last.IsZero() {
		collectedAt = a.last.Unix()
	}
	a.tableMu.RUnlock()
	if g := args["group"]; g != "" {
		f := rows[:0]
		for _, r := range rows {
			if r.Group == g {
				f = append(f, r)
			}
		}
		rows = f
	}
	sortBy := args["sort"]
	sort.Slice(rows, func(i, j int) bool {
		switch sortBy {
		case "rss", "mem":
			if rows[i].RSS != rows[j].RSS {
				return rows[i].RSS > rows[j].RSS
			}
		case "pid":
			return rows[i].PID < rows[j].PID
		default:
			if rows[i].CPU != rows[j].CPU {
				return rows[i].CPU > rows[j].CPU
			}
			if rows[i].RSS != rows[j].RSS {
				return rows[i].RSS > rows[j].RSS
			}
		}
		return rows[i].PID < rows[j].PID
	})
	total := len(rows)
	if len(rows) > a.cfg.Top {
		rows = rows[:a.cfg.Top]
	}
	out := Table{Columns: []string{"pid", "ppid", "name", "group", "cpu", "rss", "threads", "cmdline"}, Total: total, CollectedAt: collectedAt}
	out.Rows = make([]any, len(rows))
	for i, r := range rows {
		out.Rows[i] = r
	}
	return out
}

func processQueryFixture(n int) []ProcessRow {
	rows := make([]ProcessRow, n)
	for i := range rows {
		rows[i] = ProcessRow{PID: int32(i + 1), PPID: 1, Name: "fixture",
			Group: fmt.Sprintf("group%d", i%10), CPU: float64(i % 7), RSS: uint64(i%5) * 1024,
			Threads: 2, Cmdline: "fixture --sample"}
	}
	rng := rand.New(rand.NewSource(42))
	rng.Shuffle(n, func(i, j int) { rows[i], rows[j] = rows[j], rows[i] })
	return rows
}

func TestAppsProcessQueryMatchesFullSort(t *testing.T) {
	for _, n := range []int{0, 1, 19, 201, 1001} {
		original := processQueryFixture(n)
		a := &appsCollector{published: append([]ProcessRow(nil), original...), last: time.Unix(1700000000, 0)}
		for _, limit := range []int{0, 1, 2, 200, n, n + 10} {
			a.cfg.Top = limit
			for _, order := range []string{"", "cpu", "rss", "mem", "pid", "unknown"} {
				for _, group := range []string{"", "group0", "group9", "missing"} {
					args := map[string]string{"sort": order, "group": group}
					got, want := a.processes(args), referenceProcessTable(a, args)
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("n=%d top=%d sort=%q group=%q: table differs from full sort", n, limit, order, group)
					}
				}
			}
		}
		if !reflect.DeepEqual(a.published, append([]ProcessRow(nil), original...)) {
			t.Fatalf("query mutated the published snapshot for n=%d", n)
		}
	}
}

func TestAppsProcessQueryOrderingAndIsolation(t *testing.T) {
	a := &appsCollector{cfg: appsConfig{Top: 3}, published: []ProcessRow{
		{PID: 8, Group: "a", CPU: 2, RSS: 20},
		{PID: 5, Group: "a", CPU: 2, RSS: 20},
		{PID: 7, Group: "b", CPU: 1, RSS: 90},
		{PID: 3, Group: "a", CPU: 2, RSS: 10},
		{PID: 1, Group: "b", CPU: 0, RSS: 90},
	}}
	for _, tc := range []struct {
		order, group string
		ids          []int32
		total        int
	}{
		{"", "", []int32{5, 8, 3}, 5},
		{"rss", "", []int32{1, 7, 5}, 5},
		{"pid", "", []int32{1, 3, 5}, 5},
		{"mem", "a", []int32{5, 8, 3}, 3},
		{"cpu", "b", []int32{7, 1}, 2},
	} {
		table := a.processes(map[string]string{"sort": tc.order, "group": tc.group})
		ids := make([]int32, len(table.Rows))
		for i, r := range table.Rows {
			ids[i] = r.(ProcessRow).PID
		}
		if table.Total != tc.total || !reflect.DeepEqual(ids, tc.ids) {
			t.Fatalf("sort=%q group=%q: ids=%v total=%d", tc.order, tc.group, ids, table.Total)
		}
		if len(table.Rows) > 0 {
			table.Rows[0] = ProcessRow{Name: "caller edit"}
		}
	}
	if a.published[0].PID != 8 || a.published[0].Name != "" {
		t.Fatal("caller edited snapshot")
	}
}

func TestAppsProcessQueryConcurrentPublication(t *testing.T) {
	a := &appsCollector{cfg: appsConfig{Top: 10}}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for tick := int64(1); tick <= 100; tick++ {
			a.tableMu.Lock()
			a.last = time.Unix(tick, 0)
			a.published = a.published[:0]
			for pid := int32(1); pid <= 50; pid++ {
				a.published = append(a.published, ProcessRow{PID: pid, Group: "fixture", RSS: uint64(tick)})
			}
			a.tableMu.Unlock()
		}
	}()
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				table := a.processes(map[string]string{"sort": "rss", "group": "fixture"})
				if table.CollectedAt == 0 {
					if table.Total != 0 {
						t.Error("rows published before timestamp")
					}
					continue
				}
				if table.Total != 50 || len(table.Rows) != 10 {
					t.Error("mixed snapshot count")
				}
				for j, row := range table.Rows {
					r := row.(ProcessRow)
					if r.RSS != uint64(table.CollectedAt) || r.PID != int32(j+1) {
						t.Error("mixed snapshot data or ordering")
					}
				}
			}
		}()
	}
	wg.Wait()
}

func BenchmarkAppsProcessQuery(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		for _, group := range []string{"", "group0", "missing"} {
			groupName := group
			if groupName == "" {
				groupName = "all"
			}
			for _, mode := range []string{"full_sort", "top_rows"} {
				b.Run(fmt.Sprintf("n=%d/%s/%s", n, groupName, mode), func(b *testing.B) {
					a := &appsCollector{cfg: appsConfig{Top: 200}, published: processQueryFixture(n), last: time.Unix(1700000000, 0)}
					args := map[string]string{"group": group}
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						if mode == "full_sort" {
							_ = referenceProcessTable(a, args)
						} else {
							_ = a.processes(args)
						}
					}
				})
			}
		}
	}
}
