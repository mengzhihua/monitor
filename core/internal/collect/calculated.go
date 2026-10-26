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
)

// calculated items combine current chart dimensions with + - * / and parentheses.
// A reference is {chart:dimension}. This is not a general scripting language.
type calculatedConfig struct {
	Items []calculatedItem `yaml:"items"`
}

type calculatedItem struct {
	Name    string `yaml:"name"`
	Formula string `yaml:"formula"`
}

type calculatedCollector struct {
	cfg calculatedConfig
}

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
		if _, err := evalFormula(it.Formula, func(string, string) (float64, bool) { return 1, true }); err != nil && !strings.Contains(err.Error(), "missing") {
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
		})
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		_ = reg.Collect("calculated."+sanitizeID(it.Name), now, map[string]float64{"value": value})
	}
	return nil
}

type formulaParser struct {
	s    string
	i    int
	refs int
	get  func(chart, dim string) (float64, bool)
}

func evalFormula(input string, get func(chart, dim string) (float64, bool)) (float64, error) {
	p := formulaParser{s: input, get: get}
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
	switch p.s[p.i] {
	case '(':
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
	case '{':
		return p.parseRef()
	default:
		return p.parseNumber()
	}
}

func (p *formulaParser) parseRef() (float64, error) {
	if p.refs >= 8 {
		return 0, fmt.Errorf("too many references")
	}
	end := strings.IndexByte(p.s[p.i:], '}')
	if end < 0 {
		return 0, fmt.Errorf("missing }")
	}
	body := p.s[p.i+1 : p.i+end]
	p.i += end + 1
	chart, dim, ok := strings.Cut(body, ":")
	if !ok || chart == "" || dim == "" || strings.ContainsAny(chart+dim, "{}") {
		return 0, fmt.Errorf("reference must be {chart:dimension}")
	}
	p.refs++
	v, ok := p.get(chart, dim)
	if !ok {
		return 0, fmt.Errorf("missing %s:%s", chart, dim)
	}
	return v, nil
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
