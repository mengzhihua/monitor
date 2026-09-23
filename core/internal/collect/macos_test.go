package collect

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestMacosSysctlSelectionAndCustomCommand(t *testing.T) {
	for _, command := range []string{"sysctl", "/custom/sysctl", "/usr/sbin/sysctl"} {
		t.Run(command, func(t *testing.T) {
			var calls []string
			var sysArgs []string
			m := &macosCollector{cfg: macosConfig{Command: command},
				run: func(_ context.Context, name string, args ...string) ([]byte, error) {
					calls = append(calls, name)
					switch name {
					case command:
						sysArgs = append([]string(nil), args...)
						return []byte("kern.memorystatus_vm_pressure_level: 1\nvm.swapusage: used = 2.00G free = 1024.00M\n"), nil
					case "memory_pressure":
						return []byte("System-wide memory free percentage: 40%\n"), nil
					case "pmset":
						if !reflect.DeepEqual(args, []string{"-g", "batt"}) {
							t.Errorf("battery arguments changed: %v", args)
						}
						return []byte("-InternalBattery-0 81%; charged\n"), nil
					}
					return nil, errors.New("unexpected command")
				}}
			reg := registry.New(&registry.Host{Hostname: "test", UpdateEvery: 1}, nil)
			if err := m.Init(reg); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := m.Collect(context.Background(), reg, time.Unix(1700000000+int64(i), 0)); err != nil {
					t.Fatal(err)
				}
			}
			wantArgs := []string{"-a"}
			if command == "sysctl" {
				wantArgs = []string{"-i", "kern.memorystatus_vm_pressure_level", "vm.swapusage", "machdep.xcpm.cpu_thermal_level"}
			}
			if !reflect.DeepEqual(sysArgs, wantArgs) {
				t.Fatalf("sysctl arguments = %v, want %v", sysArgs, wantArgs)
			}
			if !reflect.DeepEqual(calls, []string{command, "memory_pressure", "pmset", command, "memory_pressure", "pmset", command, "memory_pressure", "pmset"}) {
				t.Fatalf("sampling cadence changed: %v", calls)
			}
			if _, ok := reg.Chart("macos.thermal_level"); ok {
				t.Fatal("missing thermal key invented a metric")
			}
			for chart, want := range map[string]map[string]float64{
				"macos.memory_pressure": {"pressure": 60},
				"macos.swap":            {"used": 2048, "free": 1024},
				"macos.battery":         {"charge": 81},
			} {
				c, ok := reg.Chart(chart)
				if !ok {
					t.Fatalf("missing chart %s", chart)
				}
				when, got := c.LastValues()
				if when != 1700000001 || !reflect.DeepEqual(got, want) {
					t.Fatalf("%s = %v at %d, want %v", chart, got, when, want)
				}
			}
		})
	}
}

func TestMacosMissingKeysAndCommandFailure(t *testing.T) {
	for _, tc := range []struct {
		name, sys string
		fail      bool
	}{
		{"empty output", "", true},
		{"empty thermal", "machdep.xcpm.cpu_thermal_level:   \n", true},
		{"malformed thermal", "machdep.xcpm.cpu_thermal_level: unavailable\n", true},
		{"available pressure", "kern.memorystatus_vm_pressure_level: 2\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &macosCollector{cfg: macosConfig{Command: "sysctl"},
				run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
					if name == "sysctl" {
						return []byte(tc.sys), nil
					}
					return nil, errors.New("unavailable")
				}}
			sample, err := m.sample(context.Background())
			if (err != nil) != tc.fail {
				t.Fatalf("sample error=%v, want failure=%v", err, tc.fail)
			}
			if !tc.fail && (sample.pressure == nil || *sample.pressure != 50 || sample.thermal != nil || sample.battery != nil) {
				t.Fatalf("wrong partial sample: %+v", sample)
			}
		})
	}
	m := &macosCollector{cfg: macosConfig{Command: "/missing/monitor-sysctl", Timeout: time.Second}}
	if _, err := m.sample(context.Background()); err == nil || !strings.Contains(err.Error(), "monitor-sysctl") {
		t.Fatalf("configured command failure was hidden: %v", err)
	}
}

func TestMacosUnavailableSampleDoesNotPublishZeros(t *testing.T) {
	available := true
	m := &macosCollector{cfg: macosConfig{Command: "sysctl"},
		run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if available && name == "sysctl" {
				return []byte("vm.swapusage: used = 256M free = 768M\n"), nil
			}
			return nil, nil // successful -i may yield no supported keys
		}}
	reg := registry.New(&registry.Host{Hostname: "test", UpdateEvery: 1}, nil)
	if err := m.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := m.Collect(context.Background(), reg, time.Unix(1000, 0)); err != nil {
		t.Fatal(err)
	}
	available = false
	if err := m.Collect(context.Background(), reg, time.Unix(1001, 0)); err == nil {
		t.Fatal("missing all metrics reported success")
	}
	chart, _ := reg.Chart("macos.swap")
	when, values := chart.LastValues()
	if when != 1000 || values["used"] != 256 || values["free"] != 768 {
		t.Fatalf("unavailable sample replaced actual data: time=%d values=%v", when, values)
	}
	available = true
	if err := m.Collect(context.Background(), reg, time.Unix(1002, 0)); err != nil {
		t.Fatal(err)
	}
	when, _ = chart.LastValues()
	if when != 1002 {
		t.Fatal("collector did not recover at the next interval")
	}
}
