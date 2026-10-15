package collect

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestSNMPNumericTypesAndMissingValues(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  float64
		ok    bool
	}{
		{"Counter32: 123456", 123456, true}, {"Counter64: 4294967496", 4294967496, true},
		{"Timeticks: (12300) 0:02:03.00", 12300, true}, {"INTEGER: up(1)", 1, true},
		{"Gauge32: 0", 0, true}, {"INTEGER: -3", -3, true},
		{"No Such Instance currently exists at this OID", 0, false}, {"STRING: unavailable", 0, false},
		{"INTEGER: NaN", 0, false}, {"No more variables left in this MIB View", 0, false},
	} {
		v, ok := snmpNumber(tc.input)
		if v != tc.want || ok != tc.ok {
			t.Fatalf("%s: %v/%v", tc.input, v, ok)
		}
	}
}

func deviceSNMP(t *testing.T, cfg snmpConfig, run func(context.Context, string, ...string) ([]byte, error)) (*snmpCollector, *registry.Registry) {
	t.Helper()
	s := &snmpCollector{run: run}
	if err := s.Configure(func(v any) error { *v.(*snmpConfig) = cfg; return nil }); err != nil {
		t.Fatal(err)
	}
	r := registry.New(&registry.Host{Hostname: "fixture", UpdateEvery: 1}, nil)
	if err := s.Init(r); err != nil {
		t.Fatal(err)
	}
	return s, r
}

func TestSNMPFleetRecoveryAndIndependentProfiles(t *testing.T) {
	var offline atomic.Bool
	offline.Store(true)
	var started atomic.Int32
	run := func(ctx context.Context, _ string, args ...string) ([]byte, error) {
		started.Add(1)
		address, oid := args[len(args)-2], args[len(args)-1]
		if address == "offline" && offline.Load() {
			return nil, errors.New("unreachable")
		}
		if oid == "1.3.6.1.2.1.1.3.0" {
			return []byte("." + oid + " = Timeticks: (500) 0:00:05.00"), nil
		}
		switch oid {
		case "1.3.6.1.2.1.33.1.2.4.0":
			return []byte("." + oid + " = INTEGER: 88"), nil
		case "1.3.6.1.2.1.33.1.2.5.0":
			return []byte("." + oid + " = INTEGER: 242"), nil
		case "1.3.6.1.2.1.33.1.4.4.1.5":
			return []byte("." + oid + ".1 = INTEGER: 140"), nil
		case "1.3.6.1.2.1.43.11.1.1.6":
			return []byte("." + oid + ".1.1 = STRING: black\n." + oid + ".2.1 = STRING: cyan\n." + oid + ".2.2 = STRING: unknown"), nil
		case "1.3.6.1.2.1.43.11.1.1.8":
			return []byte("." + oid + ".1.1 = INTEGER: 200\n." + oid + ".2.1 = INTEGER: 100\n." + oid + ".2.2 = INTEGER: 100"), nil
		case "1.3.6.1.2.1.43.11.1.1.9":
			return []byte("." + oid + ".1.1 = INTEGER: 100\n." + oid + ".2.1 = INTEGER: 25\n." + oid + ".2.2 = INTEGER: -3"), nil
		case "1.3.6.1.2.1.25.3.3.1.2":
			return []byte("." + oid + ".7 = INTEGER: 30"), nil
		case "1.3.6.1.2.1.25.2.3.1.3":
			return []byte("." + oid + ".4 = STRING: volume1"), nil
		case "1.3.6.1.2.1.25.2.3.1.4":
			return []byte("." + oid + ".4 = INTEGER: 4096"), nil
		case "1.3.6.1.2.1.25.2.3.1.5":
			return []byte("." + oid + ".4 = INTEGER: 1000"), nil
		case "1.3.6.1.2.1.25.2.3.1.6":
			return []byte("." + oid + ".4 = INTEGER: 250"), nil
		default:
			return nil, errors.New("unsupported object")
		}
	}
	s, reg := deviceSNMP(t, snmpConfig{Targets: []snmpConfig{
		{Name: "power", Address: "ups", Profile: "ups", Community: "fixture"},
		{Name: "printer", Address: "printer", Profile: "printer", Community: "fixture"},
		{Name: "nas", Address: "nas", Profile: "host", Community: "fixture"},
		{Name: "offline", Address: "offline", Profile: "ups", Community: "fixture"},
	}}, run)
	if started.Load() != 0 {
		t.Fatal("fleet init contacted devices")
	}
	if err := s.Collect(context.Background(), reg, time.Unix(100, 0)); err == nil {
		t.Fatal("offline device not reported")
	}
	for id, want := range map[string]float64{
		"power.ups.charge": 88, "power.ups.voltage": 24.2, "power.ups.output_load.1": 140,
		"printer.printer.supply_remaining.1.1": 50, "printer.printer.supply_remaining.2.1": 25,
		"nas.host.cpu_load.7": 30, "nas.host.storage_used.4": 1024000, "nas.host.storage_utilization.4": 25,
	} {
		ch, ok := reg.Chart("snmp.target." + id)
		if !ok {
			t.Fatalf("missing %s", id)
		}
		at, v := ch.LatestValues()
		if at != 100 || math.Abs(v["value"]-want) > 1e-9 {
			t.Fatalf("%s: %v at %v want %v", id, v, at, want)
		}
	}
	if _, ok := reg.Chart("snmp.target.printer.printer.supply_remaining.2.2"); ok {
		t.Fatal("unknown supply emitted as real reading")
	}
	ch, _ := reg.Chart("snmp.target.offline.target_status")
	_, v := ch.LatestValues()
	if v["down"] != 1 {
		t.Fatal(v)
	}
	offline.Store(false)
	if err := s.Collect(context.Background(), reg, time.Unix(101, 0)); err != nil {
		t.Fatal(err)
	}
	_, v = ch.LatestValues()
	if v["up"] != 1 {
		t.Fatal("offline device did not recover")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := started.Load()
	if !errors.Is(s.Collect(ctx, reg, time.Unix(102, 0)), context.Canceled) || started.Load() != before {
		t.Fatal("cancelled poll contacted devices")
	}
}

func TestSNMPHighCapacityCountersAndMissingSamples(t *testing.T) {
	cycle := -1
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		oid := args[len(args)-1]
		value := ""
		switch oid {
		case "1.3.6.1.2.1.1.3.0":
			return []byte("." + oid + " = Timeticks: (10000)"), nil
		case "1.3.6.1.2.1.2.2.1.2":
			if cycle == 3 {
				return []byte("." + oid + ".1 = STRING: renamed\n." + oid + ".2 = STRING: uplink"), nil
			}
			return []byte("." + oid + ".1 = STRING: uplink\n." + oid + ".2 = STRING: uplink"), nil
		case "1.3.6.1.2.1.2.2.1.10", "1.3.6.1.2.1.2.2.1.16":
			value = "Counter32: 12"
		case "1.3.6.1.2.1.31.1.1.1.6", "1.3.6.1.2.1.31.1.1.1.10":
			if cycle == -1 || cycle == 2 {
				return nil, errors.New("missing this time")
			}
			value = fmt.Sprintf("Counter64: %d", 5000000000+cycle*100)
		case "1.3.6.1.2.1.2.2.1.8":
			return []byte("." + oid + ".1 = INTEGER: 1"), nil
		default:
			return nil, errors.New("unsupported")
		}
		return []byte("." + oid + ".1 = " + value), nil
	}
	s, reg := deviceSNMP(t, snmpConfig{Targets: []snmpConfig{{Name: "switch", Address: "switch", Community: "fixture"}}}, run)
	for cycle = -1; cycle < 4; cycle++ {
		if err := s.Collect(context.Background(), reg, time.Unix(int64(100+cycle), 0)); err != nil {
			t.Fatal(err)
		}
		ch, _ := reg.Chart("snmp.target.switch.device_net.1")
		_, v := ch.LatestValues()
		if cycle == 3 && ch.LabelsSnapshot()["ifName"] != "renamed" {
			t.Fatal("interface rename did not update metadata")
		}
		if cycle == 0 && len(v) != 0 {
			t.Fatal("switching 32-bit to 64-bit produced a false rate", v)
		}
		if cycle == 1 || cycle == 3 {
			if v["received"] != 100 || v["sent"] != -100 {
				t.Fatalf("cycle %d: %v", cycle, v)
			}
		}
		if cycle == 2 && len(v) != 0 {
			t.Fatalf("missing HC counter fell back and fabricated data: %v", v)
		}
		other, ok := reg.Chart("snmp.target.switch.device_net.2")
		if !ok {
			t.Fatal("duplicate interface names collided")
		}
		_, v = other.LatestValues()
		if len(v) != 0 {
			t.Fatal("missing counters turned into zero", v)
		}
	}
}

func TestSNMPIndependentItemsWithoutInterfaces(t *testing.T) {
	s, reg := deviceSNMP(t, snmpConfig{Address: "fixture", Items: []snmpItem{{Name: "custom", OID: "1.3.6.1.4.1.999.0"}}}, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		oid := args[len(args)-1]
		if strings.HasSuffix(oid, "1.3.0") {
			return []byte("." + oid + " = Timeticks: (100)"), nil
		}
		if strings.HasSuffix(oid, "999.0") {
			return []byte("." + oid + " = INTEGER: 42"), nil
		}
		return nil, errors.New("no interfaces")
	})
	if err := s.Collect(context.Background(), reg, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("snmp.item.custom")
	if !ok {
		t.Fatal("interface failure skipped custom OIDs")
	}
	_, v := ch.LatestValues()
	if v["value"] != 42 {
		t.Fatal(v)
	}
}

func TestSNMPRejectsInvalidFleetConfig(t *testing.T) {
	for _, cfg := range []snmpConfig{
		{Targets: []snmpConfig{{Name: "bad.name", Address: "host"}}},
		{Targets: []snmpConfig{{Name: "same", Address: "host"}, {Name: "same", Address: "host"}}},
		{Address: "legacy", Targets: []snmpConfig{{Name: "target", Address: "host"}}},
		{Targets: []snmpConfig{{Name: "target", Address: "-bad", Community: "fixture"}}},
		{Targets: []snmpConfig{{Name: "target", Address: "host", Community: "fixture", Profile: "magic"}}},
		{Targets: []snmpConfig{{Name: "target", Address: "host", Community: "fixture", Timeout: time.Hour}}},
		{Targets: []snmpConfig{{Name: "target", Address: "host", Community: "fixture", Timeout: -time.Second}}},
		{Targets: []snmpConfig{{Name: "target", Address: "host", Community: "fixture", Targets: []snmpConfig{}}}},
		{Targets: []snmpConfig{{Name: "target", Address: "host", Version: "3", User: "monitor", PrivPass: "private"}}},
	} {
		s := &snmpCollector{}
		if err := s.Configure(func(v any) error { *v.(*snmpConfig) = cfg; return nil }); err == nil {
			t.Fatal("accepted invalid fleet config")
		}
	}
}

func TestSNMPEmptyFleetAndBoundedWorkers(t *testing.T) {
	empty, reg := deviceSNMP(t, snmpConfig{Targets: []snmpConfig{}}, func(context.Context, string, ...string) ([]byte, error) {
		t.Error("empty fleet contacted a target")
		return nil, nil
	})
	if err := empty.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(reg.Charts()) != 0 {
		t.Fatal("empty fleet created legacy charts")
	}
	var active, peak atomic.Int32
	targets := make([]snmpConfig, 9)
	for i := range targets {
		targets[i] = snmpConfig{Name: fmt.Sprint("target", i), Address: "fixture", Community: "fixture", Timeout: 50 * time.Millisecond}
	}
	s, reg := deviceSNMP(t, snmpConfig{Targets: targets}, func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err := s.Collect(context.Background(), reg, time.Now()); err == nil {
		t.Fatal("unresponsive fleet succeeded")
	}
	if active.Load() != 0 || peak.Load() > 4 || peak.Load() < 2 {
		t.Fatalf("active=%d peak=%d", active.Load(), peak.Load())
	}
}

func TestSNMPInterfaceLabelsCanRefreshWhileRead(t *testing.T) {
	s := &snmpCollector{cfg: snmpConfig{Name: "switch", Address: "fixture"}}
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	s.initCharts(reg)
	iface := snmpIface{name: "before", index: "1"}
	s.ensureIface(reg, iface, registry.Incremental)
	a, _ := reg.Chart("snmp.target.switch.device_net.1")
	b, _ := reg.Chart("snmp.target.switch.device_net_operstatus.1")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			a.LabelsSnapshot()
			b.LabelsSnapshot()
		}
	}()
	for i := 0; i < 100; i++ {
		iface.name = "after"
		s.ensureIface(reg, iface, registry.Incremental)
	}
	wg.Wait()
	if a.LabelsSnapshot()["ifName"] != "after" || b.LabelsSnapshot()["ifName"] != "after" {
		t.Fatal("labels did not refresh")
	}
}
