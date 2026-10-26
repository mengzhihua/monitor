package collect

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

// calculated items combine chart dimensions with + - * / and parentheses.
// {chart:dimension} is the latest value. avg, min, max, and sum read stored
// samples over a window from 1s to 1h. This is not a general scripting language.

// SeriesHistory reads stored samples for one series id ("chart|dimension").
type SeriesHistory interface {
	Query(id string, after, before int64) ([]tsdb.Point, error)
}

const maxCalcWindow = 3600

type calculatedConfig struct {
	Items []calculatedItem `yaml:"items"`
}

type calculatedItem struct {
	Name    string `yaml:"name"`
	Formula string `yaml:"formula"`
}

type calculatedCollector struct {
	cfg     calculatedConfig
	history SeriesHistory
}

// SetHistory supplies stored samples for avg, min, max, and sum.
func (c *calculatedCollector) SetHistory(h SeriesHistory) { c.history = h }

func init() { Register("calculated", func() Collector { return &calculatedCollector{} }) }

func (c *calculatedCollector) Name() string { return "calculated" }

func (c *calculatedCollector) Configure(decode func(v any) error) error {
	if err := decode(&c.cfg); err != nil {
		return err
	}
	if len(c.cfg.Items) > 64 {
		return fmt.Errorf("calculated: at most 64 items")
	}
	for _, it := range c.cfg.Items {
		if !validItemName(it.Name) || len(it.Formula) == 0 || len(it.Formula) > 256 {
			return fmt.Errorf("calculated: item %q needs a formula of at most 256 bytes", it.Name)
		}
		if _, err := evalFormula(it.Formula, func(string, string) (float64, bool) { return 1, true }, func(string, string, string, int) (float64, bool) {
			return 1, true
		}); err != nil && !strings.Contains(err.Error(), "missing") {
			return fmt.Errorf("calculated: item %q: %w", it.Name, err)
		}
	}
	return nil
}

func (c *calculatedCollector) Init(reg *registry.Registry) error {
	if len(c.cfg.Items) == 0 {
		return fmt.Errorf("calculated: no items")
	}
	for _, it := range c.cfg.Items {
		reg.AddChart(&registry.Chart{ID: "calculated." + sanitizeID(it.Name), Context: "calculated.value", Title: "Calculated " + it.Name,
			Units: "value", Family: "calculated", Plugin: "calculated", Module: "calculated", Priority: 56500,
			Dimensions: []*registry.Dimension{{ID: "value"}}})
	}
	return nil
}

func (c *calculatedCollector) Collect(_ context.Context, reg *registry.Registry, now time.Time) error {
	for _, it := range c.cfg.Items {
		value, err := evalFormula(it.Formula, func(chart, dim string) (float64, bool) {
			return latestDimension(reg, chart, dim)
		}, func(fn, chart, dim string, seconds int) (float64, bool) {
			return c.windowValue(fn, chart, dim, seconds, now)
		})
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		_ = reg.Collect("calculated."+sanitizeID(it.Name), now, map[string]float64{"value": value})
	}
	return nil
}

type formulaParser struct {
	s      string
	i      int
	refs   int
	get    func(chart, dim string) (float64, bool)
	window func(fn, chart, dim string, seconds int) (float64, bool)
}

func evalFormula(input string, get func(chart, dim string) (float64, bool), window func(fn, chart, dim string, seconds int) (float64, bool)) (float64, error) {
	p := formulaParser{s: input, get: get, window: window}
	v, err := p.parseExpr()
	if err != nil {
		return 0, err
	}
	p.skip()
	if p.i != len(p.s) {
		return 0, fmt.Errorf("trailing %q", p.s[p.i:])
	}
	return v, nil
}

func (p *formulaParser) parseExpr() (float64, error) {
	left, err := p.parseTerm()
	if err != nil {
		return 0, err
	}
	for {
		p.skip()
		if p.i >= len(p.s) || (p.s[p.i] != '+' && p.s[p.i] != '-') {
			return left, nil
		}
		op := p.s[p.i]
		p.i++
		right, err := p.parseTerm()
		if err != nil {
			return 0, err
		}
		if op == '+' {
			left += right
		} else {
			left -= right
		}
	}
}

func (p *formulaParser) parseTerm() (float64, error) {
	left, err := p.parseUnary()
	if err != nil {
		return 0, err
	}
	for {
		p.skip()
		if p.i >= len(p.s) || (p.s[p.i] != '*' && p.s[p.i] != '/') {
			return left, nil
		}
		op := p.s[p.i]
		p.i++
		right, err := p.parseUnary()
		if err != nil {
			return 0, err
		}
		if op == '*' {
			left *= right
		} else {
			if right == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			left /= right
		}
	}
}

func (p *formulaParser) parseUnary() (float64, error) {
	p.skip()
	if p.i < len(p.s) && p.s[p.i] == '-' {
		p.i++
		v, err := p.parseUnary()
		return -v, err
	}
	return p.parsePrimary()
}

func (p *formulaParser) parsePrimary() (float64, error) {
	p.skip()
	if p.i >= len(p.s) {
		return 0, fmt.Errorf("unexpected end")
	}
	switch {
	case p.s[p.i] == '(':
		p.i++
		v, err := p.parseExpr()
		if err != nil {
			return 0, err
		}
		p.skip()
		if p.i >= len(p.s) || p.s[p.i] != ')' {
			return 0, fmt.Errorf("missing )")
		}
		p.i++
		return v, nil
	case p.s[p.i] == '{':
		return p.parseRef()
	case p.s[p.i] >= 'a' && p.s[p.i] <= 'z':
		return p.parseCall()
	default:
		return p.parseNumber()
	}
}

func (p *formulaParser) parseCall() (float64, error) {
	start := p.i
	for p.i < len(p.s) && p.s[p.i] >= 'a' && p.s[p.i] <= 'z' {
		p.i++
	}
	name := p.s[start:p.i]
	switch name {
	case "avg", "min", "max", "sum":
	default:
		return 0, fmt.Errorf("unknown function %q", name)
	}
	p.skip()
	if p.i >= len(p.s) || p.s[p.i] != '(' {
		return 0, fmt.Errorf("%s needs parentheses", name)
	}
	p.i++
	p.skip()
	chart, dim, err := p.parseRefBody()
	if err != nil {
		return 0, err
	}
	p.skip()
	if p.i >= len(p.s) || p.s[p.i] != ',' {
		return 0, fmt.Errorf("%s needs a window", name)
	}
	p.i++
	seconds, err := p.parseWindow()
	if err != nil {
		return 0, err
	}
	p.skip()
	if p.i >= len(p.s) || p.s[p.i] != ')' {
		return 0, fmt.Errorf("missing )")
	}
	p.i++
	if p.refs >= 8 {
		return 0, fmt.Errorf("too many references")
	}
	p.refs++
	if p.window == nil {
		return 0, fmt.Errorf("missing %s:%s", chart, dim)
	}
	v, ok := p.window(name, chart, dim, seconds)
	if !ok {
		return 0, fmt.Errorf("missing %s:%s", chart, dim)
	}
	return v, nil
}

func (p *formulaParser) parseWindow() (int, error) {
	p.skip()
	start := p.i
	for p.i < len(p.s) && unicode.IsDigit(rune(p.s[p.i])) {
		p.i++
	}
	if start == p.i || p.i >= len(p.s) {
		return 0, fmt.Errorf("window must be like 5m")
	}
	n, err := strconv.Atoi(p.s[start:p.i])
	if err != nil || n < 1 || n > maxCalcWindow {
		return 0, fmt.Errorf("window must be from 1s to 1h")
	}
	unit := p.s[p.i]
	p.i++
	seconds := 0
	switch unit {
	case 's':
		seconds = n
	case 'm':
		seconds = n * 60
	case 'h':
		seconds = n * 3600
	default:
		return 0, fmt.Errorf("window must be like 5m")
	}
	if seconds < 1 || seconds > maxCalcWindow {
		return 0, fmt.Errorf("window must be from 1s to 1h")
	}
	return seconds, nil
}

func (p *formulaParser) parseRef() (float64, error) {
	chart, dim, err := p.parseRefBody()
	if err != nil {
		return 0, err
	}
	if p.refs >= 8 {
		return 0, fmt.Errorf("too many references")
	}
	p.refs++
	v, ok := p.get(chart, dim)
	if !ok {
		return 0, fmt.Errorf("missing %s:%s", chart, dim)
	}
	return v, nil
}

func (p *formulaParser) parseRefBody() (string, string, error) {
	if p.i >= len(p.s) || p.s[p.i] != '{' {
		return "", "", fmt.Errorf("reference must be {chart:dimension}")
	}
	end := strings.IndexByte(p.s[p.i:], '}')
	if end < 0 {
		return "", "", fmt.Errorf("missing }")
	}
	body := p.s[p.i+1 : p.i+end]
	p.i += end + 1
	chart, dim, ok := strings.Cut(body, ":")
	if !ok || chart == "" || dim == "" || strings.ContainsAny(chart+dim, "{}") {
		return "", "", fmt.Errorf("reference must be {chart:dimension}")
	}
	return chart, dim, nil
}

func latestDimension(reg *registry.Registry, chart, dim string) (float64, bool) {
	ch, ok := reg.Chart(chart)
	if !ok {
		return 0, false
	}
	_, vals := ch.LastValues()
	v, ok := vals[dim]
	if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

func (c *calculatedCollector) windowValue(fn, chart, dim string, seconds int, now time.Time) (float64, bool) {
	if c.history == nil {
		return 0, false
	}
	before := now.Unix()
	pts, err := c.history.Query(registry.SeriesID(chart, dim), before-int64(seconds), before)
	if err != nil {
		return 0, false
	}
	return reduceWindow(fn, pts)
}

func reduceWindow(fn string, pts []tsdb.Point) (float64, bool) {
	var n int
	var sum, lo, hi float64
	for _, p := range pts {
		if math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
			continue
		}
		if n == 0 || p.Value < lo {
			lo = p.Value
		}
		if n == 0 || p.Value > hi {
			hi = p.Value
		}
		sum += p.Value
		n++
	}
	if n == 0 {
		return 0, false
	}
	switch fn {
	case "avg":
		return sum / float64(n), true
	case "min":
		return lo, true
	case "max":
		return hi, true
	case "sum":
		return sum, true
	default:
		return 0, false
	}
}

func (p *formulaParser) parseNumber() (float64, error) {
	start := p.i
	for p.i < len(p.s) && (unicode.IsDigit(rune(p.s[p.i])) || p.s[p.i] == '.') {
		p.i++
	}
	if start == p.i {
		return 0, fmt.Errorf("unexpected %q", p.s[start:])
	}
	return strconv.ParseFloat(p.s[start:p.i], 64)
}

func (p *formulaParser) skip() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t') {
		p.i++
	}
}
