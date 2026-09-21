// Package ingest parses OpenMetrics / Prometheus text exposition and maps
// samples onto Registry charts (one chart per metric, one dimension per
// label set). Used by POST /api/v1/ingest/openmetrics and the prometheus
// scrape collector.
package ingest

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

type Kind int

const (
	KindGauge Kind = iota
	KindCounter
)

type Label struct{ Key, Value string }

type Sample struct {
	Name   string
	Labels []Label
	Value  float64
	Kind   Kind
}

type Options struct {
	Prefix    string // chart-id prefix, e.g. "prom" or "om"
	Plugin    string
	Module    string
	Family    string
	MaxCharts int
	MaxDims   int
}

func (o *Options) defaults() {
	if o.Prefix == "" {
		o.Prefix = "om"
	}
	if o.Plugin == "" {
		o.Plugin = "ingest"
	}
	if o.Module == "" {
		o.Module = o.Prefix
	}
	if o.Family == "" {
		o.Family = o.Prefix
	}
	if o.MaxCharts <= 0 {
		o.MaxCharts = 500
	}
	if o.MaxDims <= 0 {
		o.MaxDims = 200
	}
}

// Mapper turns samples into charts/dimensions, creating them on first sight.
type Mapper struct {
	opt   Options
	mu    sync.Mutex
	known map[string]int // chart id → dim count
}

func NewMapper(opt Options) *Mapper {
	opt.defaults()
	return &Mapper{opt: opt, known: map[string]int{}}
}

func (m *Mapper) Apply(reg *registry.Registry, now time.Time, samples []Sample) int {
	grouped := map[string][]Sample{}
	for _, s := range samples {
		if s.Name == "" || math.IsNaN(s.Value) || math.IsInf(s.Value, 0) {
			continue
		}
		grouped[s.Name] = append(grouped[s.Name], s)
	}
	n := 0
	for name, ss := range grouped {
		n += m.applyMetric(reg, now, name, ss)
	}
	return n
}

func (m *Mapper) applyMetric(reg *registry.Registry, now time.Time, name string, ss []Sample) int {
	id := m.opt.Prefix + "." + sanitize(name)
	m.mu.Lock()
	_, seen := m.known[id]
	if !seen && len(m.known) >= m.opt.MaxCharts {
		m.mu.Unlock()
		return 0
	}
	m.mu.Unlock()

	ch, ok := reg.Chart(id)
	if !ok {
		ch = reg.AddChart(&registry.Chart{
			ID: id, Context: m.opt.Prefix + "." + name, Family: m.opt.Family,
			Title: name, Units: unitFor(name), Priority: 80000,
			Plugin: m.opt.Plugin, Module: m.opt.Module,
			Dimensions: []*registry.Dimension{},
		})
	}
	vals := map[string]float64{}
	for _, s := range ss {
		did := dimID(s.Labels)
		if ch.Dimension(did) == nil {
			m.mu.Lock()
			if m.known[id] >= m.opt.MaxDims {
				m.mu.Unlock()
				continue
			}
			m.mu.Unlock()
			algo := registry.Absolute
			if s.Kind == KindCounter {
				algo = registry.Incremental
			}
			ch.AddDimension(&registry.Dimension{ID: did, Name: dimName(s.Labels), Algorithm: algo})
			m.mu.Lock()
			m.known[id] = m.known[id] + 1
			m.mu.Unlock()
		}
		vals[did] = s.Value
	}
	if len(vals) == 0 {
		return 0
	}
	_ = reg.Collect(id, now, vals)
	return len(vals)
}

func dimID(ls []Label) string {
	if len(ls) == 0 {
		return "value"
	}
	var b strings.Builder
	for i, l := range ls {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(l.Key)
		b.WriteByte('=')
		b.WriteString(l.Value)
	}
	s := b.String()
	if len(s) <= 80 {
		id := sanitize(s)
		if id == "" {
			return "value"
		}
		return id
	}
	sum := sha1.Sum([]byte(s))
	return sanitize(s[:40]) + "_" + hex.EncodeToString(sum[:3])
}

func dimName(ls []Label) string {
	if len(ls) == 0 {
		return "value"
	}
	parts := make([]string, 0, len(ls))
	for _, l := range ls {
		parts = append(parts, l.Value)
	}
	return strings.Join(parts, ",")
}

func unitFor(name string) string {
	switch {
	case strings.HasSuffix(name, "_bytes"):
		return "bytes"
	case strings.HasSuffix(name, "_seconds"):
		return "seconds"
	case strings.HasSuffix(name, "_total"), strings.HasSuffix(name, "_count"):
		return "events/s"
	}
	return "value"
}

func sanitize(s string) string {
	var b strings.Builder
	changed := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
			changed = true
		}
	}
	out := strings.Trim(b.String(), "._")
	if out == "" {
		return "metric"
	}
	if changed && len(s) > 12 {
		sum := sha1.Sum([]byte(s))
		out += "_" + hex.EncodeToString(sum[:3])
	}
	return out
}

// ParseOpenMetrics parses Prometheus 0.0.4 / OpenMetrics text. Histogram
// _bucket lines are kept; NaN/+Inf are dropped. Timestamps are ignored.
func ParseOpenMetrics(s string) []Sample {
	typeHint := map[string]Kind{}
	var out []Sample
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "# EOF" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			parseTypeHint(line, typeHint)
			continue
		}
		name, labels, value, ok := parseSampleLine(line)
		if !ok {
			continue
		}
		kind := typeHint[name]
		if kind == KindGauge {
			if base, ok := strings.CutSuffix(name, "_total"); ok {
				if k, seen := typeHint[base]; seen {
					kind = k
				}
			}
		}
		if t, ok := typeHint[metricFamily(name)]; ok {
			kind = t
		}
		out = append(out, Sample{Name: name, Labels: labels, Value: value, Kind: kind})
	}
	return out
}

func metricFamily(name string) string {
	for _, suf := range []string{"_bucket", "_sum", "_count", "_created", "_total"} {
		if b, ok := strings.CutSuffix(name, suf); ok {
			return b
		}
	}
	return name
}

func parseTypeHint(line string, dest map[string]Kind) {
	// # TYPE name counter|gauge|histogram|summary|untyped
	f := strings.Fields(line)
	if len(f) < 4 || f[1] != "TYPE" {
		return
	}
	switch strings.ToLower(f[3]) {
	case "counter":
		dest[f[2]] = KindCounter
	default:
		dest[f[2]] = KindGauge
	}
}

func parseSampleLine(line string) (name string, labels []Label, value float64, ok bool) {
	// name{k="v",...} value [timestamp]   OR   name value
	rest := line
	if i := strings.IndexByte(line, '{'); i >= 0 {
		name = line[:i]
		end := strings.LastIndexByte(line, '}')
		if end <= i {
			return
		}
		labels = parseLabels(line[i+1 : end])
		rest = strings.TrimSpace(line[end+1:])
	} else {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return
		}
		name, rest = fields[0], strings.Join(fields[1:], " ")
	}
	if name == "" || !validMetricName(name) {
		return
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return
	}
	return name, labels, v, true
}

func validMetricName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if !unicode.IsLetter(r) && r != '_' && r != ':' {
				return false
			}
			continue
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != ':' {
			return false
		}
	}
	return true
}

func parseLabels(s string) []Label {
	var out []Label
	s = strings.TrimSpace(s)
	for s != "" {
		eq := strings.IndexByte(s, '=')
		if eq <= 0 {
			break
		}
		key := strings.TrimSpace(s[:eq])
		s = strings.TrimSpace(s[eq+1:])
		if s == "" || s[0] != '"' {
			break
		}
		val, rest, ok := readQuoted(s)
		if !ok {
			break
		}
		out = append(out, Label{Key: key, Value: val})
		s = strings.TrimLeft(rest, " ,")
	}
	return out
}

func readQuoted(s string) (string, string, bool) {
	if s == "" || s[0] != '"' {
		return "", s, false
	}
	var b strings.Builder
	esc := false
	for i := 1; i < len(s); i++ {
		c := s[i]
		if esc {
			switch c {
			case 'n':
				b.WriteByte('\n')
			case '\\', '"':
				b.WriteByte(c)
			default:
				b.WriteByte(c)
			}
			esc = false
			continue
		}
		if c == '\\' {
			esc = true
			continue
		}
		if c == '"' {
			return b.String(), s[i+1:], true
		}
		b.WriteByte(c)
	}
	return "", s, false
}

// FormatOpenMetrics is a tiny helper for tests.
func FormatOpenMetrics(samples []Sample) string {
	var b strings.Builder
	for _, s := range samples {
		if len(s.Labels) == 0 {
			fmt.Fprintf(&b, "%s %g\n", s.Name, s.Value)
			continue
		}
		fmt.Fprintf(&b, "%s{", s.Name)
		for i, l := range s.Labels {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `%s="%s"`, l.Key, l.Value)
		}
		fmt.Fprintf(&b, "} %g\n", s.Value)
	}
	return b.String()
}
