// Package health is the alarm engine: YAML rules (alarms/templates) are
// evaluated periodically against the registry and TSDB, produce a status per
// chart, keep an alarm log and dispatch notifications on transitions.
package health

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
//     recovery: $this < 70              # stay raised until this is also true
//     for: 1m                            # condition must hold before the status rises
//     keep_firing_for: 5m               # stay raised after the condition clears
//     delay: down 15m multiplier 1.5 max 1h
//     repeat: warning 30m critical 10m
//     info: average CPU utilization over the last 10 minutes
//     macros: { CPU_WARN: "85" }         # {$NAME} expansions, override health.macros
//     to: sysadmin
type RuleSpec struct {
	Name       string            `yaml:"name" json:"name"`
	On         string            `yaml:"on" json:"on"`
	Class      string            `yaml:"class" json:"class,omitempty"`
	Type       string            `yaml:"type" json:"type,omitempty"`
	Compon     string            `yaml:"component" json:"component,omitempty"`
	Lookup     string            `yaml:"lookup" json:"lookup,omitempty"`
	Calc       string            `yaml:"calc" json:"calc,omitempty"`
	Every      string            `yaml:"every" json:"every,omitempty"`
	Units      string            `yaml:"units" json:"units,omitempty"`
	Warn       string            `yaml:"warn" json:"warn,omitempty"`
	Crit       string            `yaml:"crit" json:"crit,omitempty"`
	Recovery   string            `yaml:"recovery,omitempty" json:"recovery,omitempty"`
	For        string            `yaml:"for,omitempty" json:"for,omitempty"`
	KeepFiring string            `yaml:"keep_firing_for,omitempty" json:"keep_firing_for,omitempty"`
	Delay      string            `yaml:"delay" json:"delay,omitempty"`
	Repeat     string            `yaml:"repeat" json:"repeat,omitempty"`
	Info       string            `yaml:"info" json:"info,omitempty"`
	To         string            `yaml:"to" json:"to,omitempty"`
	Labels     map[string]string `yaml:"chart_labels" json:"chart_labels,omitempty"`
	Disabled   bool              `yaml:"disabled" json:"disabled,omitempty"`
	Macros     map[string]string `yaml:"macros" json:"macros,omitempty"` // rule-level {$NAME} overrides
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
	AnomalyBit bool   // query 0–100 anomaly rates instead of raw values
	MinMax     string // "min2max": max-min of the window
	// Zabbix-style functions. Kind is empty for the classic grouped reduce
	// above, or one of: nodata, first, change, stddev, count, trendavg,
	// trendmin, trendmax, trendsum, trendcount, forecast, timeleft.
	Kind      string
	Horizon   time.Duration // forecast: evaluate the linear fit at now+Horizon
	Target    float64       // timeleft: seconds until the fit reaches Target
	CountOp   string        // count: gt|ge|lt|le|eq|ne, empty = all points
	CountVal  float64
	hasTarget bool
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
	Spec       RuleSpec
	Lookup     *Lookup
	Every      time.Duration
	Calc       *Expr
	Warn       *Expr
	Crit       *Expr
	Recovery   *Expr
	Delay      Delay
	Repeat     Repeat
	For        time.Duration // pending time before a raise commits; 0 = immediate
	KeepFiring time.Duration // hold a raised status after the condition clears; 0 = immediate
	Info       string        // Spec.Info with macros expanded
	Units      string        // Spec.Units with macros expanded
	Source     string

	expandedHash string // hash of the macro-resolved spec (set by CompileWith)
}

var macroRef = regexp.MustCompile(`\{\$[A-Za-z_][A-Za-z0-9_]*\}`)

// ErrUnknownMacro marks {$NAME} placeholders that neither the rule-level
// macros nor the global set can resolve (including macro recursion).
var ErrUnknownMacro = errors.New("unknown macro")

// applyMacros returns a copy of spec with every {$NAME} placeholder replaced.
// Rule-level macros win over global ones; a leftover placeholder is an error.
func applyMacros(spec RuleSpec, global map[string]string) (RuleSpec, error) {
	fields := []*string{&spec.Lookup, &spec.Calc, &spec.Warn, &spec.Crit,
		&spec.Recovery, &spec.Every, &spec.For, &spec.KeepFiring, &spec.Delay,
		&spec.Repeat, &spec.Info, &spec.Units}
	// Expand iteratively so a macro value may itself contain {$OTHER}. An
	// acyclic chain resolves within #macros rounds; the +1 leaves room for
	// the no-change pass that terminates expansion.
	maxRounds := len(spec.Macros) + len(global) + 1
	for round := 0; round < maxRounds; round++ {
		changed := false
		for _, fp := range fields {
			if !strings.Contains(*fp, "{$") {
				continue
			}
			*fp = macroRef.ReplaceAllStringFunc(*fp, func(m string) string {
				name := m[2 : len(m)-1]
				if v, ok := spec.Macros[name]; ok {
					changed = true
					return v
				}
				if v, ok := global[name]; ok {
					changed = true
					return v
				}
				return m
			})
		}
		if !changed {
			break
		}
	}
	for _, fp := range fields {
		if m := macroRef.FindString(*fp); m != "" {
			name := m[2 : len(m)-1]
			if _, ok := spec.Macros[name]; ok {
				return spec, fmt.Errorf("%w: macro recursion in %s", ErrUnknownMacro, m)
			}
			if _, ok := global[name]; ok {
				return spec, fmt.Errorf("%w: macro recursion in %s", ErrUnknownMacro, m)
			}
			return spec, fmt.Errorf("%w %s", ErrUnknownMacro, m)
		}
	}
	return spec, nil
}

// Compile validates and parses a RuleSpec.
func Compile(spec RuleSpec, source string) (*Rule, error) {
	return CompileWith(spec, source, nil)
}

// CompileWith is Compile plus user-macro expansion: {$NAME} placeholders in
// the listed spec fields resolve rule-level `macros:` first, then the global
// map. The stored Spec keeps the original text so Hash() is stable.
func CompileWith(spec RuleSpec, source string, globalMacros map[string]string) (*Rule, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return nil, fmt.Errorf("%s: alarm without name", source)
	}
	orig := spec
	resolved, err := applyMacros(spec, globalMacros)
	if err != nil {
		return nil, fmt.Errorf("%s: alarm %q: %w", source, spec.Name, err)
	}
	spec = resolved
	if spec.On == "" {
		return nil, fmt.Errorf("%s: alarm %q has no `on:` chart", source, spec.Name)
	}
	r := &Rule{Spec: orig, Source: source, Every: 10 * time.Second, Info: spec.Info, Units: spec.Units}
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
	}{{&r.Calc, spec.Calc, "calc"}, {&r.Warn, spec.Warn, "warn"}, {&r.Crit, spec.Crit, "crit"}, {&r.Recovery, spec.Recovery, "recovery"}} {
		if *f.dst, err = ParseExpr(f.src); err != nil {
			return nil, fmt.Errorf("%s: alarm %q %s: %w", source, spec.Name, f.name, err)
		}
	}
	if r.Lookup == nil && r.Calc == nil {
		return nil, fmt.Errorf("%s: alarm %q needs a lookup or calc", source, spec.Name)
	}
	if r.For, err = parseSustain(source, spec.Name, "for", spec.For); err != nil {
		return nil, err
	}
	if r.KeepFiring, err = parseSustain(source, spec.Name, "keep_firing_for", spec.KeepFiring); err != nil {
		return nil, err
	}
	if r.Delay, err = ParseDelay(spec.Delay); err != nil {
		return nil, fmt.Errorf("%s: alarm %q delay: %w", source, spec.Name, err)
	}
	if r.Repeat, err = ParseRepeat(spec.Repeat); err != nil {
		return nil, fmt.Errorf("%s: alarm %q repeat: %w", source, spec.Name, err)
	}
	b, _ := yaml.Marshal(spec)
	sum := sha256.Sum256([]byte(r.Source + "\x00" + spec.Name + "\x00" + string(b)))
	r.expandedHash = hex.EncodeToString(sum[:8])
	return r, nil
}

// ExpandedHash identifies the rule after macro expansion: identical Specs
// produce different values when their global/template macros resolve
// differently. SetOverlay uses it to spot macro-only changes that Hash()
// (over the unexpanded Spec) cannot.
func (r *Rule) ExpandedHash() string {
	if r == nil {
		return ""
	}
	return r.expandedHash
}

// Hash is a stable id for GET /api/v3/alert_config?hash= (Netdata-style).
func (r *Rule) Hash() string {
	if r == nil {
		return ""
	}
	b, _ := yaml.Marshal(r.Spec)
	sum := sha256.Sum256([]byte(r.Source + "\x00" + r.Spec.Name + "\x00" + string(b)))
	return hex.EncodeToString(sum[:8])
}

// ParseLookup parses "<method> <-duration> [unaligned] [absolute] [percentage] [anomaly-bit] [of dim1,dim2]".
//
// Classic methods reduce raw samples: average|avg|mean, min, max, sum
// (rate-integrated), median, last, min2max (max-min). Zabbix-style functions
// (Kind) are also accepted; the window is [now-After, now] and the result is
// summed across the selected `of` dimensions unless noted:
//
//	nodata -5m                     1 if no non-NaN point exists in the window, else 0 (never NaN)
//	first  -5m                     oldest value in the window
//	change -5m                     last - first value in the window
//	stddev -5m                     population stddev of the window
//	count  -5m [gt|ge|lt|le|eq|ne <n>]   non-NaN points matching the operator
//	trendavg|trendmin|trendmax|trendsum|trendcount -1d
//	                               rollup-tier equivalents of average/min/max/sum/count
//	forecast -1h horizon 30m       least-squares linear fit evaluated at now+horizon
//	timeleft -1h target 0          seconds until the fit reaches target (min across dims);
//	                               returns the finite sentinel 1e15 when the trend never gets there
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
	case "nodata", "first", "change", "stddev", "count", "forecast", "timeleft":
		l.Kind = strings.ToLower(f[0])
	case "trendavg", "trendmin", "trendmax", "trendsum", "trendcount":
		l.Kind = strings.ToLower(f[0])
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
		opt := strings.ToLower(f[i])
		switch opt {
		case "unaligned", "aligned":
		case "absolute", "abs", "absolute_sum", "absolute-sum":
			l.AbsValue = true
		case "percentage", "percent":
			l.Percentage = true
		case "anomaly-bit", "anomaly_bit", "anomalybit":
			l.AnomalyBit = true
		case "horizon", "target", "gt", "ge", "lt", "le", "eq", "ne":
			if i+1 >= len(f) {
				return nil, fmt.Errorf("missing value after %q", f[i])
			}
			v := f[i+1]
			i++
			switch opt {
			case "horizon":
				h, err := parseDuration(v)
				if err != nil || h <= 0 {
					return nil, fmt.Errorf("bad horizon %q", v)
				}
				l.Horizon = h
			case "target":
				t, err := strconv.ParseFloat(v, 64)
				if err != nil {
					return nil, fmt.Errorf("bad target %q", v)
				}
				l.Target, l.hasTarget = t, true
			default: // count comparison operator
				if l.Kind != "count" {
					return nil, fmt.Errorf("option %q only applies to count", opt)
				}
				n, err := strconv.ParseFloat(v, 64)
				if err != nil {
					return nil, fmt.Errorf("bad count operand %q", v)
				}
				l.CountOp, l.CountVal = opt, n
			}
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
	if l.Kind == "forecast" && l.Horizon <= 0 {
		return nil, fmt.Errorf("forecast requires `horizon <duration>`")
	}
	if l.Kind == "timeleft" && !l.hasTarget {
		return nil, fmt.Errorf("timeleft requires `target <number>`")
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
	if len(f)%2 != 0 {
		return r, fmt.Errorf("repeat %q: expected \"<warning|critical> <duration>\" pairs", s)
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

func parseSustain(source, name, field, raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	d, err := parseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: alarm %q %s: %w", source, name, field, err)
	}
	if d < 0 || d > 24*time.Hour {
		return 0, fmt.Errorf("%s: alarm %q %s must be from 0s to 24h", source, name, field)
	}
	return d, nil
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
	return LoadDirWith(dir, nil)
}

// LoadDirWith is LoadDir plus global user-macro expansion.
func LoadDirWith(dir string, macros map[string]string) ([]*Rule, error) {
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
		rs, err := ParseRulesWith(b, filepath.Join(dir, n), macros)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rs...)
	}
	return rules, nil
}

// ParseRules parses a YAML document `alarms: [...]` (or a bare list).
func ParseRules(b []byte, source string) ([]*Rule, error) {
	return ParseRulesWith(b, source, nil)
}

// ParseRulesWith is ParseRules plus global user-macro expansion.
func ParseRulesWith(b []byte, source string, macros map[string]string) ([]*Rule, error) {
	var f ruleFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		var list []RuleSpec
		if err2 := yaml.Unmarshal(b, &list); err2 != nil {
			return nil, fmt.Errorf("%s: %w", source, err)
		}
		f.Alarms = list
	}
	return CompileAllWith(f.Alarms, source, macros)
}

// CompileAll compiles a list of specs, stopping at the first error.
func CompileAll(specs []RuleSpec, source string) ([]*Rule, error) {
	return CompileAllWith(specs, source, nil)
}

// CompileAllWith is CompileAll plus global user-macro expansion.
func CompileAllWith(specs []RuleSpec, source string, macros map[string]string) ([]*Rule, error) {
	rules := make([]*Rule, 0, len(specs))
	for _, spec := range specs {
		r, err := CompileWith(spec, source, macros)
		if err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, nil
}
