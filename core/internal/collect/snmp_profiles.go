package collect

import (
	"context"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// These profiles use standard MIBs, not vendor-specific guesses. Unsupported
// objects and unknown sentinels are absent samples rather than zero readings.
func (s *snmpCollector) collectProfile(ctx context.Context, reg *registry.Registry, now time.Time) {
	switch s.cfg.Profile {
	case "ups":
		for _, spec := range []struct {
			name, oid, units string
			scale            float64
		}{
			{"battery_status", "1.3.6.1.2.1.33.1.2.1.0", "state", 1},
			{"runtime", "1.3.6.1.2.1.33.1.2.3.0", "minutes", 1},
			{"charge", "1.3.6.1.2.1.33.1.2.4.0", "%", 1},
			{"voltage", "1.3.6.1.2.1.33.1.2.5.0", "volts", .1},
			{"temperature", "1.3.6.1.2.1.33.1.2.7.0", "Celsius", 1},
		} {
			b, err := s.walk(ctx, spec.oid)
			if err != nil || ctx.Err() != nil {
				continue
			}
			if v, ok := parseSNMPWalk(b)["0"]; ok {
				s.profileValue(reg, now, "ups."+spec.name, spec.units, "", "", v*spec.scale)
			}
		}
		for index, v := range s.numberTable(ctx, "1.3.6.1.2.1.33.1.4.4.1.5") {
			// RFC 1628 permits output load above 100%, up to 200%.
			if v >= 0 && ctx.Err() == nil {
				s.profileValue(reg, now, "ups.output_load", "%", index, "", v)
			}
		}
	case "printer":
		base := "1.3.6.1.2.1.43.11.1.1."
		names := s.stringTable(ctx, base+"6")
		capacity := s.numberTable(ctx, base+"8")
		levels := s.numberTable(ctx, base+"9")
		for index, level := range levels {
			max, ok := capacity[index]
			// -1/-2/-3 are RFC 3805 unknown/other/some-left sentinels.
			if !ok || max <= 0 || level < 0 || ctx.Err() != nil {
				continue
			}
			s.profileValue(reg, now, "printer.supply_remaining", "%", index, names[index], level/max*100)
		}
	case "host":
		for index, v := range s.numberTable(ctx, "1.3.6.1.2.1.25.3.3.1.2") {
			if v >= 0 && v <= 100 && ctx.Err() == nil {
				s.profileValue(reg, now, "host.cpu_load", "%", index, "", v)
			}
		}
		base := "1.3.6.1.2.1.25.2.3.1."
		names := s.stringTable(ctx, base+"3")
		units := s.numberTable(ctx, base+"4")
		sizes := s.numberTable(ctx, base+"5")
		used := s.numberTable(ctx, base+"6")
		for index, v := range used {
			total, ok := sizes[index]
			unit, unitOK := units[index]
			if !ok || !unitOK || total <= 0 || unit <= 0 || v < 0 || ctx.Err() != nil {
				continue
			}
			s.profileValue(reg, now, "host.storage_used", "bytes", index, names[index], v*unit)
			s.profileValue(reg, now, "host.storage_total", "bytes", index, names[index], total*unit)
			s.profileValue(reg, now, "host.storage_utilization", "%", index, names[index], v/total*100)
		}
	}
}

func (s *snmpCollector) numberTable(ctx context.Context, oid string) map[string]float64 {
	b, err := s.walk(ctx, oid)
	if err != nil {
		return nil
	}
	return snmpTableNumbers(b, oid)
}

func (s *snmpCollector) stringTable(ctx context.Context, oid string) map[string]string {
	b, err := s.walk(ctx, oid)
	if err != nil {
		return nil
	}
	values := snmpTableStrings(b, oid)
	for k, v := range values {
		values[k] = snmpString(v)
	}
	return values
}

func snmpString(v string) string {
	if _, rest, ok := strings.Cut(v, ":"); ok {
		v = rest
	}
	return strings.Trim(strings.TrimSpace(v), `"`)
}

func (s *snmpCollector) profileValue(reg *registry.Registry, now time.Time, metric, units, index, name string, value float64) {
	context := "snmp." + metric
	id := context
	if index != "" {
		id += "." + index
	}
	id = s.chartID(id)
	if !s.seen[id] {
		s.seen[id] = true
		labels := s.deviceLabels()
		if index != "" {
			labels["index"] = index
		}
		if name != "" {
			labels["name"] = name
		}
		reg.AddChart(&registry.Chart{ID: id, Context: context, Title: "SNMP " + strings.ReplaceAll(metric, ".", " "), Units: units,
			Family: s.cfg.Profile, Plugin: "snmp", Module: "snmp", Priority: 56200, Labels: labels, Dimensions: []*registry.Dimension{{ID: "value"}}})
	}
	_ = reg.Collect(id, now, map[string]float64{"value": value})
}
