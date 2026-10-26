package collect

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestPingCountReportsLossAndAverageRTT(t *testing.T) {
	openHost, openPort, openClose := serveTCP(t, 0)
	defer openClose()
	onceHost, oncePort, onceClose := serveTCP(t, 1)
	defer onceClose()

	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	p := &pingCollector{}
	if err := p.Configure(func(v any) error {
		*v.(*pingConfig) = pingConfig{Timeout: 200 * time.Millisecond, Jobs: []pingJob{
			{Name: "open", Host: openHost, Protocol: "tcp", Port: openPort, Count: 3, Timeout: 200 * time.Millisecond},
			{Name: "once", Host: onceHost, Protocol: "tcp", Port: oncePort, Count: 3, Timeout: 200 * time.Millisecond},
			{Name: "closed", Host: "127.0.0.1", Protocol: "tcp", Port: 1, Count: 3, Timeout: 200 * time.Millisecond},
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := p.Collect(context.Background(), reg, time.Unix(40, 0)); err != nil {
		t.Fatal(err)
	}
	_, openLoss := mustChart(t, reg, "ping.loss.open").LastValues()
	if openLoss["received"] != 3 || openLoss["dropped"] != 0 {
		t.Fatalf("open loss %+v", openLoss)
	}
	_, openRTT := mustChart(t, reg, "ping.latency.open").LastValues()
	if openRTT["rtt"] <= 0 {
		t.Fatalf("open rtt %+v", openRTT)
	}
	_, onceLoss := mustChart(t, reg, "ping.loss.once").LastValues()
	if onceLoss["received"] != 1 || onceLoss["dropped"] != 2 {
		t.Fatalf("once loss %+v", onceLoss)
	}
	_, onceRTT := mustChart(t, reg, "ping.latency.once").LastValues()
	if onceRTT["rtt"] <= 0 {
		t.Fatalf("once rtt %+v", onceRTT)
	}
	_, closedLoss := mustChart(t, reg, "ping.loss.closed").LastValues()
	_, closedRTT := mustChart(t, reg, "ping.latency.closed").LastValues()
	if closedLoss["received"] != 0 || closedLoss["dropped"] != 3 || closedRTT["rtt"] != 0 {
		t.Fatalf("closed loss %+v rtt %+v", closedLoss, closedRTT)
	}

	bad := &pingCollector{}
	err := bad.Configure(func(v any) error {
		*v.(*pingConfig) = pingConfig{Jobs: []pingJob{{Name: "gw", Host: "127.0.0.1", Count: 6}}}
		return nil
	})
	if err == nil {
		t.Fatal("accepted count 6")
	}
	one := &pingCollector{}
	if err := one.Configure(func(v any) error {
		*v.(*pingConfig) = pingConfig{Jobs: []pingJob{{Name: "gw", Host: "127.0.0.1"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if one.cfg.Jobs[0].Count != 1 {
		t.Fatalf("default count %d", one.cfg.Jobs[0].Count)
	}
}

// serveTCP accepts connections until n have been served. n <= 0 accepts until close.
func serveTCP(t *testing.T, n int) (host string, port int, closeFn func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var got atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			got.Add(1)
			_ = c.Close()
			if n > 0 && int(got.Load()) >= n {
				_ = ln.Close()
				return
			}
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port, func() { _ = ln.Close() }
}
