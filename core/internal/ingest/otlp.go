package ingest

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// OTLP JSON (ExportMetricsServiceRequest) plus a tiny protobuf walker for the
// common gauge/sum datapoint shape. Histogram/summary are skipped.

type otlpExport struct {
	ResourceMetrics []otlpResourceMetrics `json:"resourceMetrics"`
}

type otlpResourceMetrics struct {
	Resource     otlpResource      `json:"resource"`
	ScopeMetrics []otlpScopeMetric `json:"scopeMetrics"`
}

type otlpResource struct {
	Attributes []otlpKV `json:"attributes"`
}

type otlpScopeMetric struct {
	Metrics []otlpMetric `json:"metrics"`
}

type otlpMetric struct {
	Name        string      `json:"name"`
	Unit        string      `json:"unit"`
	Gauge       *otlpNumber `json:"gauge"`
	Sum         *otlpNumber `json:"sum"`
	Description string      `json:"description"`
}

type otlpNumber struct {
	DataPoints  []otlpPoint `json:"dataPoints"`
	IsMonotonic bool        `json:"isMonotonic"`
	Aggregation string      `json:"aggregationTemporality"`
}

type otlpPoint struct {
	Attributes []otlpKV    `json:"attributes"`
	AsDouble   *float64    `json:"asDouble"`
	AsInt      json.Number `json:"asInt"`
	Value      *float64    `json:"value"`
}

type otlpKV struct {
	Key   string  `json:"key"`
	Value otlpAny `json:"value"`
}

type otlpAny struct {
	StringValue string      `json:"stringValue"`
	IntValue    json.Number `json:"intValue"`
	DoubleValue *float64    `json:"doubleValue"`
	BoolValue   *bool       `json:"boolValue"`
}

// ParseOTLP maps an OTLP metrics payload (JSON or protobuf) onto ingest samples.
func ParseOTLP(body []byte, contentType string) ([]Sample, error) {
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "json") || looksJSON(body) {
		return parseOTLPJSON(body)
	}
	if strings.Contains(ct, "protobuf") || strings.Contains(ct, "proto") {
		return parseOTLPProto(body)
	}
	if s, err := parseOTLPJSON(body); err == nil && len(s) > 0 {
		return s, nil
	}
	return parseOTLPProto(body)
}

func looksJSON(b []byte) bool {
	for _, c := range b {
		if c == ' ' || c == '\n' || c == '\r' || c == '\t' {
			continue
		}
		return c == '{' || c == '['
	}
	return false
}

func parseOTLPJSON(body []byte) ([]Sample, error) {
	var exp otlpExport
	if err := json.Unmarshal(body, &exp); err != nil {
		return nil, fmt.Errorf("otlp json: %w", err)
	}
	var out []Sample
	for _, rm := range exp.ResourceMetrics {
		res := kvLabels(rm.Resource.Attributes)
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				out = append(out, metricSamples(m, res)...)
			}
		}
	}
	return out, nil
}

func metricSamples(m otlpMetric, resource []Label) []Sample {
	name := strings.ReplaceAll(m.Name, ".", "_")
	name = strings.ReplaceAll(name, "/", "_")
	if name == "" {
		return nil
	}
	var pts []otlpPoint
	kind := KindGauge
	if m.Gauge != nil {
		pts = m.Gauge.DataPoints
	}
	if m.Sum != nil {
		pts = m.Sum.DataPoints
		if m.Sum.IsMonotonic || strings.Contains(strings.ToLower(m.Sum.Aggregation), "cumulative") {
			kind = KindCounter
		}
	}
	var out []Sample
	for _, p := range pts {
		v, ok := pointValue(p)
		if !ok {
			continue
		}
		labels := append([]Label{}, resource...)
		labels = append(labels, kvLabels(p.Attributes)...)
		out = append(out, Sample{Name: name, Labels: labels, Value: v, Kind: kind})
	}
	return out
}

func pointValue(p otlpPoint) (float64, bool) {
	if p.AsDouble != nil {
		return *p.AsDouble, true
	}
	if p.Value != nil {
		return *p.Value, true
	}
	if p.AsInt != "" {
		n, err := p.AsInt.Float64()
		return n, err == nil
	}
	return 0, false
}

func kvLabels(kvs []otlpKV) []Label {
	var out []Label
	for _, kv := range kvs {
		if kv.Key == "" {
			continue
		}
		v := kv.Value.StringValue
		if v == "" && kv.Value.IntValue != "" {
			v = kv.Value.IntValue.String()
		}
		if v == "" && kv.Value.DoubleValue != nil {
			v = strconv.FormatFloat(*kv.Value.DoubleValue, 'f', -1, 64)
		}
		if v == "" && kv.Value.BoolValue != nil {
			if *kv.Value.BoolValue {
				v = "true"
			} else {
				v = "false"
			}
		}
		if v == "" {
			continue
		}
		out = append(out, Label{Key: kv.Key, Value: v})
	}
	return out
}

// parseOTLPProto walks ExportMetricsServiceRequest enough to extract
// ResourceMetrics → ScopeMetrics → Metric → Gauge/Sum NumberDataPoint.
func parseOTLPProto(body []byte) ([]Sample, error) {
	var out []Sample
	err := walkProto(body, func(field int, wt int, v protoVal) {
		if field != 1 || wt != 2 { // resource_metrics
			return
		}
		parseResourceMetrics(v.bytes, &out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func parseResourceMetrics(b []byte, out *[]Sample) {
	var resource []Label
	_ = walkProto(b, func(field int, wt int, v protoVal) {
		switch field {
		case 1: // resource
			if wt == 2 {
				resource = parseResourceAttrs(v.bytes)
			}
		case 2: // scope_metrics
			if wt == 2 {
				_ = walkProto(v.bytes, func(f int, w int, pv protoVal) {
					if f == 2 && w == 2 { // metrics
						parseMetricProto(pv.bytes, resource, out)
					}
				})
			}
		}
	})
}

func parseResourceAttrs(b []byte) []Label {
	var labels []Label
	_ = walkProto(b, func(field int, wt int, v protoVal) {
		if field == 1 && wt == 2 { // attributes
			if k, val, ok := parseKVProto(v.bytes); ok {
				labels = append(labels, Label{Key: k, Value: val})
			}
		}
	})
	return labels
}

func parseMetricProto(b []byte, resource []Label, out *[]Sample) {
	var name string
	_ = walkProto(b, func(field int, wt int, v protoVal) {
		switch {
		case field == 1 && wt == 2:
			name = string(v.bytes)
		case field == 5 && wt == 2: // gauge
			parseNumberProto(name, KindGauge, resource, v.bytes, out)
		case field == 7 && wt == 2: // sum
			kind := KindGauge
			_ = walkProto(v.bytes, func(f int, w int, pv protoVal) {
				if f == 3 && w == 0 && pv.u64 == 1 { // is_monotonic
					kind = KindCounter
				}
			})
			parseNumberProto(name, kind, resource, v.bytes, out)
		}
	})
}

func parseNumberProto(name string, kind Kind, resource []Label, b []byte, out *[]Sample) {
	name = strings.ReplaceAll(strings.ReplaceAll(name, ".", "_"), "/", "_")
	if name == "" {
		return
	}
	_ = walkProto(b, func(field int, wt int, v protoVal) {
		if field != 1 || wt != 2 { // data_points
			return
		}
		var labels []Label
		labels = append(labels, resource...)
		var value float64
		var has bool
		_ = walkProto(v.bytes, func(f int, w int, pv protoVal) {
			switch {
			case f == 7 && w == 2: // attributes (NumberDataPoint.attributes = 7)
				if k, val, ok := parseKVProto(pv.bytes); ok {
					labels = append(labels, Label{Key: k, Value: val})
				}
			case f == 4 && w == 1: // as_double
				value = pv.f64
				has = true
			case f == 6 && (w == 0 || w == 1): // as_int (int64 or sfixed64)
				if w == 1 {
					value = float64(int64(pv.u64))
				} else {
					value = float64(int64(pv.u64))
				}
				has = true
			}
		})
		if has {
			*out = append(*out, Sample{Name: name, Labels: labels, Value: value, Kind: kind})
		}
	})
}

func parseKVProto(b []byte) (key, val string, ok bool) {
	_ = walkProto(b, func(field int, wt int, v protoVal) {
		switch {
		case field == 1 && wt == 2:
			key = string(v.bytes)
		case field == 2 && wt == 2: // AnyValue
			_ = walkProto(v.bytes, func(f int, w int, pv protoVal) {
				switch {
				case f == 1 && w == 2:
					val = string(pv.bytes)
				case f == 2 && w == 0:
					val = strconv.FormatBool(pv.u64 != 0)
				case f == 3 && w == 0:
					val = strconv.FormatInt(int64(pv.u64), 10)
				case f == 4 && w == 1:
					val = strconv.FormatFloat(pv.f64, 'f', -1, 64)
				}
			})
		}
	})
	return key, val, key != "" && val != ""
}

type protoVal struct {
	u64   uint64
	f64   float64
	bytes []byte
}

func walkProto(b []byte, fn func(field, wt int, v protoVal)) error {
	i := 0
	for i < len(b) {
		key, n := consumeVarint(b[i:])
		if n == 0 {
			return fmt.Errorf("otlp proto: bad tag")
		}
		i += n
		field := int(key >> 3)
		wt := int(key & 7)
		var v protoVal
		switch wt {
		case 0: // varint
			u, m := consumeVarint(b[i:])
			if m == 0 {
				return fmt.Errorf("otlp proto: bad varint")
			}
			i += m
			v.u64 = u
		case 1: // 64-bit
			if i+8 > len(b) {
				return fmt.Errorf("otlp proto: truncated fixed64")
			}
			u := uint64(b[i]) | uint64(b[i+1])<<8 | uint64(b[i+2])<<16 | uint64(b[i+3])<<24 |
				uint64(b[i+4])<<32 | uint64(b[i+5])<<40 | uint64(b[i+6])<<48 | uint64(b[i+7])<<56
			v.u64 = u
			v.f64 = math.Float64frombits(u)
			i += 8
		case 2: // len
			ln, m := consumeVarint(b[i:])
			if m == 0 || i+m+int(ln) > len(b) {
				return fmt.Errorf("otlp proto: truncated bytes")
			}
			i += m
			v.bytes = b[i : i+int(ln)]
			i += int(ln)
		case 5: // 32-bit
			if i+4 > len(b) {
				return fmt.Errorf("otlp proto: truncated fixed32")
			}
			i += 4
		default:
			return fmt.Errorf("otlp proto: wire type %d", wt)
		}
		fn(field, wt, v)
	}
	return nil
}

func consumeVarint(b []byte) (uint64, int) {
	var x uint64
	for i := 0; i < len(b) && i < 10; i++ {
		x |= uint64(b[i]&0x7f) << (7 * i)
		if b[i] < 0x80 {
			return x, i + 1
		}
	}
	return 0, 0
}
