package registry

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
)

func TestChartsByContextTracksDefinitions(t *testing.T) {
	r := newReg(nil)
	r.AddChart(&Chart{ID: "disk.b", Context: "disk.space", Priority: 2})
	r.AddChart(&Chart{ID: "other", Context: "disk.io", Priority: 1})
	r.AddChart(&Chart{ID: "disk.c", Context: "disk.space", Priority: 1})
	r.AddChart(&Chart{ID: "disk.a", Context: "disk.space", Priority: 2})
	r.AddChart(&Chart{ID: "default.context"})
	check := func(context string, want []string) {
		t.Helper()
		got := []string{}
		for _, c := range r.ChartsByContext(context) {
			got = append(got, c.ID)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("context %q = %v, want %v", context, got, want)
		}
	}
	check("disk.space", []string{"disk.c", "disk.a", "disk.b"})
	check("disk", []string{}) // Exact context matching, not a prefix.
	check("default.context", []string{"default.context"})
	snapshot := r.ChartsByContext("disk.space")
	snapshot[0] = nil // Callers own the slice, not the registry's membership.
	check("disk.space", []string{"disk.c", "disk.a", "disk.b"})
	r.ReplaceChart(&Chart{ID: "disk.a", Context: "disk.io", Priority: 1})
	r.ReplaceChart(&Chart{ID: "other", Context: "disk.space", Priority: 1})
	r.RemoveChart("disk.b")
	check("disk.space", []string{"disk.c", "other"})
	check("disk.io", []string{"disk.a"})
	if snapshot[1].Context != "disk.space" {
		t.Fatal("replacing a definition mutated a prior snapshot")
	}
}

func TestChartsByContextConcurrentDefinitions(t *testing.T) {
	r := newReg(nil)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 200 {
			id := fmt.Sprintf("disk.%d", i%8)
			r.AddChart(&Chart{ID: id, Context: "disk.space"})
			r.ReplaceChart(&Chart{ID: id, Context: "disk.io"})
			r.RemoveChart(id)
		}
	}()
	for range 200 {
		for _, context := range []string{"disk.space", "disk.io"} {
			for _, c := range r.ChartsByContext(context) {
				if c.Context != context {
					t.Errorf("wrong context %q, want %q", c.Context, context)
				}
			}
		}
	}
	wg.Wait()
}
