package collect

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
	psnet "github.com/shirou/gopsutil/v4/net"
)

func TestNetstatCombinedSnapshotPreservesCountsAndStates(t *testing.T) {
	tcp := []psnet.ConnectionStat{
		{Family: 2, Type: 1, Status: "LISTEN"},
		{Family: 30, Type: 1, Status: "ESTABLISHED"},
		{Family: 2, Type: 1},
	}
	udp := []psnet.ConnectionStat{{Family: 2, Type: 2}, {Family: 30, Type: 2}}
	unix := []psnet.ConnectionStat{{Family: 1, Type: 1}, {Family: 1, Type: 2}}
	var previous map[string]float64
	for _, combined := range []bool{false, true} {
		t.Run(fmt.Sprint(combined), func(t *testing.T) {
			var calls []string
			n := &netstatCollector{readConnections: func(_ context.Context, kind string) ([]psnet.ConnectionStat, error) {
				calls = append(calls, kind)
				switch kind {
				case "tcp":
					return tcp, nil
				case "udp":
					return udp, nil
				case "unix":
					return unix, nil
				case "inet":
					rows := append(append([]psnet.ConnectionStat{}, tcp...), udp...)
					// A defensive check prevents Unix SOCK_STREAM from becoming TCP.
					return append(rows, unix...), nil
				}
				panic(kind)
			}}
			reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
			if err := n.Init(reg); err != nil {
				t.Fatal(err)
			}
			calls = nil
			if err := n.collectSockets(context.Background(), reg, time.Unix(100, 0), combined); err != nil {
				t.Fatal(err)
			}
			wantCalls := []string{"tcp", "udp", "unix"}
			if combined {
				wantCalls = []string{"inet", "unix"}
			}
			if !reflect.DeepEqual(calls, wantCalls) {
				t.Fatalf("calls=%v", calls)
			}
			ch, _ := reg.Chart("ip.sockstat")
			_, counts := ch.LastValues()
			if !reflect.DeepEqual(counts, map[string]float64{"tcp": 3, "udp": 2, "unix": 2}) {
				t.Fatalf("counts=%v", counts)
			}
			ch, _ = reg.Chart("ip.tcpsock")
			_, states := ch.LastValues()
			if states["listen"] != 1 || states["established"] != 1 || states["close"] != 1 {
				t.Fatalf("states=%v", states)
			}
			if previous != nil && !reflect.DeepEqual(states, previous) {
				t.Fatalf("states changed: %v != %v", states, previous)
			}
			previous = states
		})
	}
}

func TestNetstatFailedSourcesLeaveMissingValues(t *testing.T) {
	fail := map[string]bool{}
	n := &netstatCollector{readConnections: func(_ context.Context, kind string) ([]psnet.ConnectionStat, error) {
		if fail[kind] {
			return nil, errors.New("unavailable")
		}
		return nil, nil
	}}
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	if err := n.Init(reg); err != nil {
		t.Fatal(err)
	}
	fail["unix"], fail["udp"] = true, true
	if err := n.collectSockets(context.Background(), reg, time.Unix(100, 0), false); err != nil {
		t.Fatal(err)
	}
	ch, _ := reg.Chart("ip.sockstat")
	_, values := ch.LastValues()
	if !reflect.DeepEqual(values, map[string]float64{"tcp": 0}) {
		t.Fatalf("failed sources became zero: %v", values)
	}
	fail["inet"] = true
	if err := n.collectSockets(context.Background(), reg, time.Unix(101, 0), true); err == nil {
		t.Fatal("failed snapshot accepted")
	}
	if ts, _ := ch.LastValues(); ts != 100 {
		t.Fatal("failed snapshot created a sample")
	}
	clear(fail)
	if err := n.collectSockets(context.Background(), reg, time.Unix(102, 0), true); err != nil {
		t.Fatal(err)
	}
	_, values = ch.LastValues()
	if !reflect.DeepEqual(values, map[string]float64{"tcp": 0, "udp": 0, "unix": 0}) {
		t.Fatalf("recovery: %v", values)
	}
}

func TestNetworkFunctionOnlyResolvesReturnedPIDsAndCachesFailures(t *testing.T) {
	for _, deny := range []bool{false, true} {
		t.Run(fmt.Sprint(deny), func(t *testing.T) {
			calls := map[int32]int{}
			n := &netstatCollector{top: 2,
				readConnections: func(context.Context, string) ([]psnet.ConnectionStat, error) {
					return []psnet.ConnectionStat{
						{Type: 1, Pid: 99, Status: "LISTEN"},
						{Type: 1, Pid: 2, Status: "ESTABLISHED"},
						{Type: 1, Pid: 1, Status: "ESTABLISHED", Laddr: psnet.Addr{IP: "127.0.0.1", Port: 11}},
						{Type: 1, Pid: 1, Status: "ESTABLISHED", Laddr: psnet.Addr{IP: "127.0.0.1", Port: 10}},
					}, nil
				},
				readProcess: func(_ context.Context, pid int32) (string, string) {
					calls[pid]++
					if deny {
						return "", ""
					}
					return "fixture", strings.Repeat("x", 250)
				},
			}
			table, err := n.connections(context.Background(), map[string]string{"state": "ESTABLISHED"})
			if err != nil || table.Total != 3 || len(table.Rows) != 2 {
				t.Fatalf("table=%+v err=%v", table, err)
			}
			if !reflect.DeepEqual(calls, map[int32]int{1: 1}) {
				t.Fatalf("lookups=%v", calls)
			}
			row := table.Rows[0].(ConnRow)
			if row.Local != "127.0.0.1:10" {
				t.Fatalf("order=%+v", row)
			}
			if !deny && (row.Name != "fixture" || len(row.Cmdline) != 200) {
				t.Fatalf("details=%+v", row)
			}
			if deny && (row.Name != "" || row.Cmdline != "") {
				t.Fatal("inaccessible process details fabricated")
			}
		})
	}
}

func TestNetstatCanceledScanCannotBecomeAnEmptySuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	n := &netstatCollector{readConnections: func(context.Context, string) ([]psnet.ConnectionStat, error) {
		calls++
		cancel()
		return nil, nil
	}}
	if _, err := n.read(ctx, "inet"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled scan: %v", err)
	}
	if _, err := n.read(ctx, "inet"); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("already canceled: %v calls=%d", err, calls)
	}
}

func BenchmarkNetstatSocketScans(b *testing.B) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "freebsd" {
		b.Skip("lsof platforms")
	}
	for _, combined := range []bool{false, true} {
		b.Run(fmt.Sprint(combined), func(b *testing.B) {
			n := &netstatCollector{}
			reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
			if err := n.Init(reg); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				err := n.collectSockets(ctx, reg, time.Unix(int64(i+1), 0), combined)
				cancel()
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
