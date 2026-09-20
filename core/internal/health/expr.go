package health

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// Expr is a parsed alarm expression. The grammar follows Netdata's health
// expressions: numbers, $variables, + - * / %, comparisons, && || !, the
// ternary operator and the functions abs(), min(), max(), isnan(), isinf().
type Expr struct {
	root node
	vars []string
}

type node interface {
	eval(vars func(string) (float64, bool)) float64
}

type numNode float64

func (n numNode) eval(func(string) (float64, bool)) float64 { return float64(n) }

type varNode string

func (n varNode) eval(vars func(string) (float64, bool)) float64 {
	if v, ok := vars(string(n)); ok {
		return v
	}
	return math.NaN()
}

type unaryNode struct {
	op string
	x  node
}

func (n unaryNode) eval(vars func(string) (float64, bool)) float64 {
	v := n.x.eval(vars)
	switch n.op {
	case "-":
		return -v
	case "!":
		return b2f(!truthy(v))
	}
	return v
}

type binNode struct {
	op   string
	l, r node
}

func (n binNode) eval(vars func(string) (float64, bool)) float64 {
	switch n.op {
	case "&&":
		return b2f(truthy(n.l.eval(vars)) && truthy(n.r.eval(vars)))
	case "||":
		return b2f(truthy(n.l.eval(vars)) || truthy(n.r.eval(vars)))
	}
	l, r := n.l.eval(vars), n.r.eval(vars)
	switch n.op {
	case "+":
		return l + r
	case "-":
		return l - r
	case "*":
		return l * r
	case "/":
		if r == 0 {
			return math.NaN()
		}
		return l / r
	case "%":
		if r == 0 {
			return math.NaN()
		}
		return math.Mod(l, r)
	case "==":
		return b2f(l == r || (math.IsNaN(l) && math.IsNaN(r)))
	case "!=":
		return b2f(!(l == r || (math.IsNaN(l) && math.IsNaN(r))))
	case "<":
		return b2f(l < r)
	case "<=":
		return b2f(l <= r)
	case ">":
		return b2f(l > r)
	case ">=":
		return b2f(l >= r)
	}
	return math.NaN()
}

type condNode struct{ c, a, b node }

func (n condNode) eval(vars func(string) (float64, bool)) float64 {
	if truthy(n.c.eval(vars)) {
		return n.a.eval(vars)
	}
	return n.b.eval(vars)
}

type callNode struct {
	fn   string
	args []node
}

func (n callNode) eval(vars func(string) (float64, bool)) float64 {
	a := make([]float64, len(n.args))
	for i, x := range n.args {
		a[i] = x.eval(vars)
	}
	switch n.fn {
	case "abs":
		return math.Abs(a[0])
	case "isnan":
		return b2f(math.IsNaN(a[0]))
	case "isinf":
		return b2f(math.IsInf(a[0], 0))
	case "min":
		m := a[0]
		for _, v := range a[1:] {
			m = math.Min(m, v)
		}
		return m
	case "max":
		m := a[0]
		for _, v := range a[1:] {
			m = math.Max(m, v)
		}
		return m
	}
	return math.NaN()
}

func truthy(v float64) bool { return !math.IsNaN(v) && v != 0 }

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// Eval evaluates the expression; unknown variables evaluate to NaN.
func (e *Expr) Eval(vars func(string) (float64, bool)) float64 {
	if e == nil || e.root == nil {
		return math.NaN()
	}
	return e.root.eval(vars)
}

// Vars lists the variable names referenced by the expression.
func (e *Expr) Vars() []string {
	if e == nil {
		return nil
	}
	return e.vars
}

// ParseExpr compiles src; an empty source yields a nil *Expr.
func ParseExpr(src string) (*Expr, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return nil, nil
	}
	p := &parser{toks: nil}
	if err := p.lex(src); err != nil {
		return nil, err
	}
	root, err := p.parseTernary()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.toks) {
		return nil, fmt.Errorf("unexpected %q", p.toks[p.pos].s)
	}
	return &Expr{root: root, vars: p.vars}, nil
}

type token struct {
	kind byte // 'n' number, 'v' variable, 'i' identifier, 'o' operator
	s    string
	n    float64
}

type parser struct {
	toks []token
	pos  int
	vars []string
}

var opChars = "+-*/%<>=!&|?:(),"

func (p *parser) lex(src string) error {
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c >= '0' && c <= '9' || c == '.':
			j := i
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.' || src[j] == 'e' || src[j] == 'E' ||
				((src[j] == '-' || src[j] == '+') && j > i && (src[j-1] == 'e' || src[j-1] == 'E'))) {
				j++
			}
			n, err := strconv.ParseFloat(src[i:j], 64)
			if err != nil {
				return fmt.Errorf("bad number %q", src[i:j])
			}
			p.toks = append(p.toks, token{kind: 'n', s: src[i:j], n: n})
			i = j
		case c == '$':
			j := i + 1
			if j < len(src) && src[j] == '{' {
				k := strings.IndexByte(src[j:], '}')
				if k < 0 {
					return fmt.Errorf("unterminated ${ in %q", src)
				}
				name := src[j+1 : j+k]
				p.toks = append(p.toks, token{kind: 'v', s: name})
				p.vars = append(p.vars, name)
				i = j + k + 1
				continue
			}
			for j < len(src) && isIdent(rune(src[j])) {
				j++
			}
			if j == i+1 {
				return fmt.Errorf("empty variable name at %d", i)
			}
			name := src[i+1 : j]
			p.toks = append(p.toks, token{kind: 'v', s: name})
			p.vars = append(p.vars, name)
			i = j
		case isIdentStart(rune(c)):
			j := i
			for j < len(src) && isIdent(rune(src[j])) {
				j++
			}
			word := src[i:j]
			switch strings.ToLower(word) {
			case "and":
				p.toks = append(p.toks, token{kind: 'o', s: "&&"})
			case "or":
				p.toks = append(p.toks, token{kind: 'o', s: "||"})
			case "not":
				p.toks = append(p.toks, token{kind: 'o', s: "!"})
			case "nan":
				p.toks = append(p.toks, token{kind: 'n', s: word, n: math.NaN()})
			case "inf":
				p.toks = append(p.toks, token{kind: 'n', s: word, n: math.Inf(1)})
			default:
				p.toks = append(p.toks, token{kind: 'i', s: word})
			}
			i = j
		case strings.IndexByte(opChars, c) >= 0:
			if i+1 < len(src) {
				two := src[i : i+2]
				switch two {
				case "&&", "||", "==", "!=", "<=", ">=":
					p.toks = append(p.toks, token{kind: 'o', s: two})
					i += 2
					continue
				}
			}
			p.toks = append(p.toks, token{kind: 'o', s: string(c)})
			i++
		default:
			return fmt.Errorf("unexpected character %q at %d", c, i)
		}
	}
	return nil
}

func isIdentStart(r rune) bool { return unicode.IsLetter(r) || r == '_' }
func isIdent(r rune) bool      { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' }

func (p *parser) peek() *token {
	if p.pos < len(p.toks) {
		return &p.toks[p.pos]
	}
	return nil
}

func (p *parser) accept(op string) bool {
	if t := p.peek(); t != nil && t.kind == 'o' && t.s == op {
		p.pos++
		return true
	}
	return false
}

func (p *parser) expect(op string) error {
	if !p.accept(op) {
		if t := p.peek(); t != nil {
			return fmt.Errorf("expected %q, got %q", op, t.s)
		}
		return fmt.Errorf("expected %q at end of expression", op)
	}
	return nil
}

func (p *parser) parseTernary() (node, error) {
	c, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if !p.accept("?") {
		return c, nil
	}
	a, err := p.parseTernary()
	if err != nil {
		return nil, err
	}
	if err := p.expect(":"); err != nil {
		return nil, err
	}
	b, err := p.parseTernary()
	if err != nil {
		return nil, err
	}
	return condNode{c, a, b}, nil
}

func (p *parser) parseBinary(next func() (node, error), ops ...string) (node, error) {
	l, err := next()
	if err != nil {
		return nil, err
	}
	for {
		matched := ""
		for _, op := range ops {
			if p.accept(op) {
				matched = op
				break
			}
		}
		if matched == "" {
			return l, nil
		}
		r, err := next()
		if err != nil {
			return nil, err
		}
		l = binNode{matched, l, r}
	}
}

func (p *parser) parseOr() (node, error)  { return p.parseBinary(p.parseAnd, "||") }
func (p *parser) parseAnd() (node, error) { return p.parseBinary(p.parseCmp, "&&") }
func (p *parser) parseCmp() (node, error) {
	return p.parseBinary(p.parseAdd, "==", "!=", "<=", ">=", "<", ">")
}
func (p *parser) parseAdd() (node, error) { return p.parseBinary(p.parseMul, "+", "-") }
func (p *parser) parseMul() (node, error) { return p.parseBinary(p.parseUnary, "*", "/", "%") }

func (p *parser) parseUnary() (node, error) {
	if p.accept("-") {
		x, err := p.parseUnary()
		return unaryNode{"-", x}, err
	}
	if p.accept("+") {
		return p.parseUnary()
	}
	if p.accept("!") {
		x, err := p.parseUnary()
		return unaryNode{"!", x}, err
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (node, error) {
	t := p.peek()
	if t == nil {
		return nil, fmt.Errorf("unexpected end of expression")
	}
	switch t.kind {
	case 'n':
		p.pos++
		return numNode(t.n), nil
	case 'v':
		p.pos++
		return varNode(t.s), nil
	case 'i':
		p.pos++
		fn := strings.ToLower(t.s)
		if err := p.expect("("); err != nil {
			return nil, fmt.Errorf("unknown identifier %q", t.s)
		}
		var args []node
		if !p.accept(")") {
			for {
				a, err := p.parseTernary()
				if err != nil {
					return nil, err
				}
				args = append(args, a)
				if p.accept(")") {
					break
				}
				if err := p.expect(","); err != nil {
					return nil, err
				}
			}
		}
		switch fn {
		case "abs", "isnan", "isinf":
			if len(args) != 1 {
				return nil, fmt.Errorf("%s() takes one argument", fn)
			}
		case "min", "max":
			if len(args) < 1 {
				return nil, fmt.Errorf("%s() needs at least one argument", fn)
			}
		default:
			return nil, fmt.Errorf("unknown function %q", t.s)
		}
		return callNode{fn, args}, nil
	case 'o':
		if t.s == "(" {
			p.pos++
			x, err := p.parseTernary()
			if err != nil {
				return nil, err
			}
			if err := p.expect(")"); err != nil {
				return nil, err
			}
			return x, nil
		}
	}
	return nil, fmt.Errorf("unexpected %q", t.s)
}
