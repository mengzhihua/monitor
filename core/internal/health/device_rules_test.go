package health

import (
	"math"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func TestDefaultDeviceRulesThresholdsIsolationAndMissingData(t *testing.T) {
	rules, err := DefaultRules()
	if err != nil {
		t.Fatal(err)
	}
	type step struct {
		value float64
		want  Status
	}
	for _, tc := range []struct {
		name, context, dimension string
		healthy                  float64
		steps                    []step
	}{
		{"snmp_target_unreachable", "snmp.target_status", "down", 0, []step{{0, StatusClear}, {1, StatusCritical}, {0, StatusClear}}},
		{"redfish_target_collection_failed", "redfish.target_status", "down", 0, []step{{0, StatusClear}, {1, StatusCritical}, {0, StatusClear}}},
		{"snmp_ups_battery_state", "snmp.ups.battery_status", "value", 2, []step{{2, StatusClear}, {3, StatusWarning}, {4, StatusCritical}, {1, StatusUndefined}, {0, StatusUndefined}, {2, StatusClear}}},
		{"snmp_printer_supply_remaining", "snmp.printer.supply_remaining", "value", 80, []step{{10.1, StatusClear}, {10, StatusWarning}, {5, StatusCritical}, {0, StatusCritical}, {-3, StatusUndefined}, {50, StatusClear}}},
		{"snmp_host_storage_utilization", "snmp.host.storage_utilization", "value", 30, []step{{84.9, StatusClear}, {85, StatusWarning}, {95, StatusCritical}, {-1, StatusUndefined}, {30, StatusClear}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			reg := registry.New(&registry.Host{Hostname: "device-test", UpdateEvery: 15}, db)
			ids := []string{"snmp.target.alpha." + tc.context, "snmp.target.beta." + tc.context}
			for _, id := range ids {
				reg.AddChart(&registry.Chart{ID: id, Context: tc.context, Dimensions: []*registry.Dimension{{ID: tc.dimension}, {ID: "other"}}})
			}
			// A similar chart ID with an unrelated context must not inherit the rule.
			reg.AddChart(&registry.Chart{ID: "unrelated." + tc.context, Context: "unrelated.device", Dimensions: []*registry.Dimension{{ID: tc.dimension}}})
			e, err := New(reg, db, Options{Rules: rules, LogDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(e.Close)
			now := time.Unix(1_700_000_000, 0)
			assert := func(want Status) {
				t.Helper()
				alarms := e.Alarms()
				if len(alarms) != 2 {
					t.Fatalf("expected one alarm per target, got %+v", alarms)
				}
				for _, a := range alarms {
					expected := StatusClear
					if a.Chart == ids[0] {
						expected = want
					}
					if a.Name != tc.name || a.Status != expected {
						t.Fatalf("alarm %s/%s = %s (value %v), want %s", a.Name, a.Chart, a.Status, a.Value, expected)
					}
					if expected == StatusUndefined && !math.IsNaN(a.Value) {
						t.Fatalf("missing/unknown data retained a real value: %+v", a)
					}
				}
			}
			// No initial sample must not become a synthetic failure or a clear result.
			_ = reg.Collect(ids[1], now, map[string]float64{tc.dimension: tc.healthy})
			e.Tick(now)
			assert(StatusUndefined)
			for _, st := range tc.steps {
				now = now.Add(40 * time.Second)
				_ = reg.Collect(ids[0], now, map[string]float64{tc.dimension: st.value})
				_ = reg.Collect(ids[1], now, map[string]float64{tc.dimension: tc.healthy})
				e.Tick(now)
				assert(st.want)
			}
			// The chart remains alive but this dimension is absent for a full lookup window.
			now = now.Add(40 * time.Second)
			_ = reg.Collect(ids[0], now, map[string]float64{"other": 1})
			_ = reg.Collect(ids[1], now, map[string]float64{tc.dimension: tc.healthy})
			e.Tick(now)
			assert(StatusUndefined)
		})
	}
}

func TestDefaultRedfishHealthDoesNotClearUnknownState(t *testing.T) {
	rules, err := DefaultRules()
	if err != nil {
		t.Fatal(err)
	}
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	reg := registry.New(&registry.Host{Hostname: "device-test", UpdateEvery: 15}, db)
	const id = "redfish.system_health_state.bmc.system_1"
	reg.AddChart(&registry.Chart{ID: id, Context: "redfish.system_health_state", Dimensions: []*registry.Dimension{{ID: "critical"}, {ID: "unknown"}}})
	e, err := New(reg, db, Options{Rules: rules, LogDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	now := time.Unix(1_700_000_000, 0)
	for _, tc := range []struct {
		values map[string]float64
		want   Status
	}{
		{map[string]float64{"critical": 1, "unknown": 0}, StatusWarning},
		{map[string]float64{"critical": 0, "unknown": 1}, StatusUndefined},
		{map[string]float64{"critical": 0, "unknown": 0}, StatusClear},
		// Older streamed agents may not publish the new unknown dimension.
		{map[string]float64{"critical": 1}, StatusWarning},
	} {
		now = now.Add(40 * time.Second)
		_ = reg.Collect(id, now, tc.values)
		e.Tick(now)
		alarms := e.Alarms()
		if len(alarms) != 1 || alarms[0].Name != "redfish_system_critical" || alarms[0].Status != tc.want {
			t.Fatalf("values=%v alarms=%+v want=%s", tc.values, alarms, tc.want)
		}
	}
}
