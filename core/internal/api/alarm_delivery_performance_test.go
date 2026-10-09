package api

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/mengzhihua/monitor/core/internal/health"
)

func TestLiveAlarmDeliveryScopeAndSharedEncoding(t *testing.T) {
	first := &liveConn{node: "agent", send: make(chan []byte, 1)}
	second := &liveConn{node: "agent", charts: map[string]bool{"unrelated": true}, send: make(chan []byte, 1)}
	local := &liveConn{send: make(chan []byte, 1)}
	slow := &liveConn{node: "agent", send: make(chan []byte)}
	s := &Server{live: &liveHub{conns: map[*liveConn]struct{}{first: {}, second: {}, local: {}, slow: {}}}}
	e := health.LogEntry{AlarmID: 42, Chart: "system.cpu", Name: "high", Status: health.StatusUndefined, Value: math.NaN(), OldValue: math.Inf(1)}
	s.PublishNodeAlarm("agent", e)
	if len(first.send) != 1 || len(second.send) != 1 || len(local.send) != 0 {
		t.Fatal("alarm scope must follow the node, regardless of chart subscriptions")
	}
	a, b := <-first.send, <-second.send
	if &a[0] != &b[0] {
		t.Fatal("alarm recipients did not share one encoding")
	}
	var got struct {
		Node  string          `json:"node"`
		Alarm health.LogEntry `json:"alarm"`
	}
	if err := json.Unmarshal(a, &got); err != nil || got.Node != "agent" || got.Alarm.AlarmID != e.AlarmID || got.Alarm.Status != e.Status {
		t.Fatalf("alarm payload = %+v, error = %v", got, err)
	}
}

func TestLiveUnobservedAlarmDoesNotAllocate(t *testing.T) {
	for _, clients := range []bool{false, true} {
		h := &liveHub{conns: map[*liveConn]struct{}{}}
		if clients {
			h.conns[&liveConn{node: "other", send: make(chan []byte, 1)}] = struct{}{}
		}
		s := &Server{live: h}
		e := health.LogEntry{AlarmID: 1, Name: "high", Status: health.StatusWarning}
		if allocations := testing.AllocsPerRun(100, func() { s.PublishAlarm(e) }); allocations != 0 {
			t.Fatalf("unobserved alarm allocated %g objects (other-node clients=%t)", allocations, clients)
		}
	}
}

func BenchmarkLiveAlarmDelivery(b *testing.B) {
	for _, scenario := range []string{"no_clients", "other_node", "subscribed"} {
		b.Run(scenario, func(b *testing.B) {
			c := &liveConn{send: make(chan []byte, 1)}
			h := &liveHub{conns: map[*liveConn]struct{}{}}
			if scenario != "no_clients" {
				h.conns[c] = struct{}{}
			}
			if scenario == "other_node" {
				c.node = "other"
			}
			s := &Server{live: h}
			e := health.LogEntry{AlarmID: 1, Name: "cpu_high", Chart: "system.cpu", Status: health.StatusWarning,
				OldStatus: health.StatusClear, Value: 85, OldValue: 20, When: 1700000000}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.PublishAlarm(e)
				if scenario == "subscribed" {
					<-c.send
				}
			}
		})
	}
}
