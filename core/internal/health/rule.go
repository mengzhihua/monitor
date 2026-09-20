// Package health is the alarm engine: YAML rules (alarms/templates) are
// evaluated periodically against the registry and TSDB, produce a status per
// chart, keep an alarm log and dispatch notifications on transitions.
package health

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

// RuleSpec is one alarm definition as written in YAML (health.d/*.yaml or
// the `health.alarms` config section). Field semantics follow Netdata's
// health configuration so existing knowledge transfers directly:
//
//   - name: 10min_cpu_usage
//     on: system.cpu                     # chart context (template) or chart id
//     lookup: average -10m of user,system,softirq,irq,guest
//     every: 1m
//     units: '%'
//     warn: $this > (($status >= $WARNING) ? (75) : (85))
//     crit: $this > (($status == $CRITICAL) ? (85) : (95))
//     delay: down 15m multiplier 1.5 max 1h
//     repeat: warning 30m critical 10m
//     info: average CPU utilization over the last 10 minutes
//     to: sysadmin
type RuleSpec struct {
	Name     string            `yaml:"name"`
	On       string            `yaml:"on"`
	Class    string            `yaml:"class"`
	Type     string            `yaml:"type"`
	Compon   string            `yaml:"component"`
	Lookup   string            `yaml:"lookup"`
	Calc     string            `yaml:"calc"`
	Every    string            `yaml:"every"`
	Units    string            `yaml:"units"`
	Warn     string            `yaml:"warn"`
	Crit     string            `yaml:"crit"`
	Delay    string            `yaml:"delay"`
	Repeat   string            `yaml:"repeat"`
	Info     string            `yaml:"info"`
	To       string            `yaml:"to"`
	Labels   map[string]string `yaml:"chart_labels"`
	Disabled bool              `yaml:"disabled"`
}

type ruleFile struct {
	Alarms []RuleSpec `yaml:"alarms"`
}

// Lookup describes the database query feeding $this.
type Lookup struct {
	Method     tsdb.GroupFunc
	After      time.Duration // positive; window is [now-After, now]
	Dimensions []string      // empty = all
	Percentage bool          // result as % of the sum of all dimensions
	AbsValue   bool
	MinMax     string // "min2max": max-min of the window
}

// Delay postpones notifications so flapping alarms do not spam (Netdata's
// `delay:` line). Up applies when severity rises, Down when it falls; each
// consecutive change multiplies the delay, capped by Max.
type Delay struct {
	Up, Down   time.Duration
	Multiplier float64
	Max        time.Duration
}

// Repeat re-sends notifications while an alarm stays raised.
type Repeat struct {
	Warning, Critical time.Duration
}

// Rule is a compiled RuleSpec.
type Rule struct {
	Spec   RuleSpec
	Lookup *Lookup
	Every  time.Duration
	Calc   *Expr
	Warn   *Expr
	Crit   *Expr
	Delay  Delay
	Repeat Repeat
	Source string
}

// Compile validates and parses a RuleSpec.
func Compile(spec RuleSpec, source string) (*Rule, error) {
	if spec.Name == "" {
		return nil, fmt.Errorf("%s: alarm without name", source)
	}
	if spec.On == "" {
		return nil, fmt.Errorf("%s: alarm %q has no `on:` chart", source, spec.Name)
	}
	r := &Rule{Spec: spec, Source: source, Every: 10 * time.Second}
	var err error
	if spec.Lookup != "" {
		if r.Lookup, err = ParseLookup(spec.Lookup); err != nil {
			return nil, fmt.Errorf("%s: alarm %q lookup: %w", source, spec.Name, err)
		}
	}
	if spec.Every != "" {
		if r.Every, err = parseDuration(spec.Every); err != nil {
			return nil, fmt.Errorf("%s: alarm %q every: %w", source, spec.Name, err)
		}
		if r.Every < time.Second {
			r.Every = time.Second
		}
	}
	for _, f := range []struct {
		dst  **Expr
		src  string
		name string
	}{{&r.Calc, spec.Calc, "calc"}, {&r.Warn, spec.Warn, "warn"}, {&r.Crit, spec.Crit, "crit"}} {
		if *f.dst, err = ParseExpr(f.src); err != nil {
			return nil, fmt.Errorf("%s: alarm %q %s: %w", source, spec.Name, f.name, err)
		}
	}
	if r.Lookup == nil && r.Calc == nil {
		return nil, fmt.Errorf("%s: alarm %q needs a lookup or calc", source, spec.Name)
	}
	if r.Delay, err = ParseDelay(spec.Delay); err != nil {
		return nil, fmt.Errorf("%s: alarm %q delay: %w", source, spec.Name, err)
	}
	if r.Repeat, err = ParseRepeat(spec.Repeat); err != nil {
		return nil, fmt.Errorf("%s: alarm %q repeat: %w", source, spec.Name, err)
	}
	return r, nil
}

// ParseLookup parses "<method> <-duration> [unaligned] [absolute] [percentage] [of dim1,dim2]".
func ParseLookup(s string) (*Lookup, error) {
	f := strings.Fields(s)
	if len(f) < 2 {
		return nil, fmt.Errorf("want `<method> <duration> [of dims]`, got %q", s)
	}
	l := &Lookup{}
	switch strings.ToLower(f[0]) {
	case "average", "avg", "mean":
		l.Method = tsdb.GroupAverage
	case "min":
		l.Method = tsdb.GroupMin
	case "max":
		l.Method = tsdb.GroupMax
	case "sum":
		l.Method = tsdb.GroupSum
	case "median":
		l.Method = tsdb.GroupMedian
	case "last":
		l.Method = tsdb.GroupLast
	case "min2max":
		l.Method, l.MinMax = tsdb.GroupMax, "min2max"
	default:
		return nil, fmt.Errorf("unknown method %q", f[0])
	}
	d, err := parseDuration(strings.TrimPrefix(f[1], "-"))
	if err != nil {
		return nil, err
	}
	if d <= 0 {
		return nil, fmt.Errorf("duration must be positive")
	}
	l.After = d
	for i := 2; i < len(f); i++ {
		switch strings.ToLower(f[i]) {
		case "unaligned", "aligned":
		case "absolute", "abs", "absolute_sum", "absolute-sum":
			l.AbsValue = true
		case "percentage", "percent":
			l.Percentage = true
		case "of":
			rest := strings.Join(f[i+1:], "")
			if rest != "" && rest != "*" {
				for _, dim := range strings.Split(rest, ",") {
					if dim = strings.TrimSpace(dim); dim != "" {
						l.Dimensions = append(l.Dimensions, dim)
					}
				}
			}
			i = len(f)
		default:
			return nil, fmt.Errorf("unknown option %q", f[i])
		}
	}
	return l, nil
}

// ParseDelay parses "up 30s down 15m multiplier 1.5 max 1h".
func ParseDelay(s string) (Delay, error) {
	d := Delay{Multiplier: 1}
	f := strings.Fields(s)
	for i := 0; i < len(f); i++ {
		if i+1 >= len(f) {
			return d, fmt.Errorf("missing value after %q", f[i])
		}
		v := f[i+1]
		var err error
		switch strings.ToLower(f[i]) {
		case "up":
			d.Up, err = parseDuration(v)
		case "down":
			d.Down, err = parseDuration(v)
		case "max":
			d.Max, err = parseDuration(v)
		case "multiplier":
			d.Multiplier, err = strconv.ParseFloat(v, 64)
		default:
			return d, fmt.Errorf("unknown keyword %q", f[i])
		}
		if err != nil {
			return d, err
		}
		i++
	}
	if d.Multiplier < 1 {
		d.Multiplier = 1
	}
	if d.Max == 0 {
		d.Max = time.Hour
	}
	return d, nil
}

// ParseRepeat parses "off" or "warning 30m critical 10m".
func ParseRepeat(s string) (Repeat, error) {
	var r Repeat
	f := strings.Fields(s)
	if len(f) == 0 || strings.EqualFold(f[0], "off") {
		return r, nil
	}
	for i := 0; i+1 < len(f); i += 2 {
		d, err := parseDuration(f[i+1])
		if err != nil {
			return r, err
		}
		switch strings.ToLower(f[i]) {
		case "warning", "warn":
			r.Warning = d
		case "critical", "crit":
			r.Critical = d
		default:
			return r, fmt.Errorf("unknown keyword %q", f[i])
		}
	}
	return r, nil
}

// parseDuration accepts Go durations plus bare seconds and the d suffix.
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return time.Duration(n * float64(time.Second)), nil
	}
	if strings.HasSuffix(s, "d") {
		n, err := strconv.ParseFloat(strings.TrimSuffix(s, "d"), 64)
		if err != nil {
			return 0, fmt.Errorf("bad duration %q", s)
		}
		return time.Duration(n * float64(24*time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("bad duration %q", s)
	}
	return d, nil
}

// LoadDir reads every *.yaml/*.yml in dir (non-recursively, sorted). A
// missing directory yields no rules and no error.
func LoadDir(dir string) ([]*Rule, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if ext := strings.ToLower(filepath.Ext(e.Name())); ext == ".yaml" || ext == ".yml" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var rules []*Rule
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return nil, err
		}
		rs, err := ParseRules(b, filepath.Join(dir, n))
		if err != nil {
			return nil, err
		}
		rules = append(rules, rs...)
	}
	return rules, nil
}

// ParseRules parses a YAML document `alarms: [...]` (or a bare list).
func ParseRules(b []byte, source string) ([]*Rule, error) {
	var f ruleFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		var list []RuleSpec
		if err2 := yaml.Unmarshal(b, &list); err2 != nil {
			return nil, fmt.Errorf("%s: %w", source, err)
		}
		f.Alarms = list
	}
	return CompileAll(f.Alarms, source)
}

// CompileAll compiles a list of specs, stopping at the first error.
func CompileAll(specs []RuleSpec, source string) ([]*Rule, error) {
	rules := make([]*Rule, 0, len(specs))
	for _, spec := range specs {
		r, err := Compile(spec, source)
		if err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, nil
}
