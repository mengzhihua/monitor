package api

import (
	"encoding/json"
	"math"
	"testing"
)

func TestLiveBroadcastFiltersBeforeEncoding(t *testing.T) {
	local := &liveConn{send: make(chan []byte, 4)}
	selected := &liveConn{charts: map[string]bool{"system.cpu": true}, send: make(chan []byte, 4)}
	remote := &liveConn{node: "remote", send: make(chan []byte, 4)}
	slow := &liveConn{send: make(chan []byte)}
	h := &liveHub{conns: map[*liveConn]struct{}{local: {}, selected: {}, remote: {}, slow: {}}}
	values := map[string]float64{"used": 23}
	h.broadcast("system.ram", 100, values)
	if len(local.send) != 1 || len(selected.send) != 0 || len(remote.send) != 0 {
		t.Fatal("sample delivered outside the chart/node subscription")
	}
	var got liveMsg
	if err := json.Unmarshal(<-local.send, &got); err != nil || got.Node != "" || got.Chart != "system.ram" || got.T != 100 || got.Values["used"] != 23 {
		t.Fatalf("local message = %+v, error = %v", got, err)
	}
	selected.setCharts([]string{"system.ram"})
	selected.setNode("remote")
	h.broadcastNode("remote", "system.ram", 101, values)
	if len(local.send) != 0 || len(selected.send) != 1 || len(remote.send) != 1 {
		t.Fatal("updated subscription was not applied")
	}
	if err := json.Unmarshal(<-remote.send, &got); err != nil || got.Node != "remote" || got.T != 101 {
		t.Fatalf("remote message = %+v, error = %v", got, err)
	}
	<-selected.send
	h.broadcastNode("remote", "system.ram", 102, map[string]float64{"used": math.NaN()})
	if len(selected.send) != 0 || len(remote.send) != 0 {
		t.Fatal("invalid JSON values must not be sent")
	}
}

func TestLiveUnsubscribedSampleDoesNotAllocate(t *testing.T) {
	c := &liveConn{charts: map[string]bool{"system.cpu": true}, send: make(chan []byte, 1)}
	h := &liveHub{conns: map[*liveConn]struct{}{c: {}}}
	values := map[string]float64{"used": 23}
	if allocations := testing.AllocsPerRun(100, func() { h.broadcast("system.ram", 100, values) }); allocations != 0 {
		t.Fatalf("unsubscribed sample allocated %g objects", allocations)
	}
}
