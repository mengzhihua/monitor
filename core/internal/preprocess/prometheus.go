package preprocess

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

const (
	maxPromPattern = 256
	maxPromBytes   = 1 << 20
	maxPromLines   = 20000
	maxPromLine    = 8192
	maxPromMatches = 4096
	maxPromLabels  = 8
)

type promMatcher struct {
	key   string
	op    string
	value string
	re    *regexp.Regexp
}

type promPattern struct {
	name     string
	matchers []promMatcher
}

type promHit struct {
	labels map[string]string
	value  float64
}

func applyPrometheus(st Step, text string) (string, error) {
	if len(text) > maxPromBytes {
		return "", fmt.Errorf("prometheus: input too large")
	}
	pattern, err := parsePromPattern(st.Pattern)
	if err != nil {
		return "", err
	}
	output := strings.ToLower(strings.TrimSpace(st.Output))
	if output == "" {
		output = "value"
	}
	switch output {
	case "value", "sum", "min", "max", "avg", "count", "label":
	default:
		return "", fmt.Errorf("prometheus: output must be value, sum, min, max, avg, count, or label")
	}
	labelName := st.Path
	if output == "label" && !validPromLabel(labelName) {
		return "", fmt.Errorf("prometheus: label output needs a label name in path")
	}
	hits, err := promHits(text, pattern)
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return "", fmt.Errorf("prometheus: no match")
	}
	switch output {
	case "value":
		if len(hits) != 1 {
			return "", fmt.Errorf("prometheus: multiple matches")
		}
		return formatProm(hits[0].value), nil
	case "label":
		if len(hits) != 1 {
			return "", fmt.Errorf("prometheus: multiple matches")
		}
		value, ok := hits[0].labels[labelName]
		if !ok {
			return "", fmt.Errorf("prometheus: label %s missing", labelName)
		}
		return value, nil
	case "count":
		return strconv.Itoa(len(hits)), nil
	}
	total := 0.0
	lo, hi := hits[0].value, hits[0].value
	for _, hit := range hits {
		total += hit.value
		if hit.value < lo {
			lo = hit.value
		}
		if hit.value > hi {
			hi = hit.value
		}
	}
	switch output {
	case "sum":
		return formatProm(total), nil
	case "min":
		return formatProm(lo), nil
	case "max":
		return formatProm(hi), nil
	case "avg":
		return formatProm(total / float64(len(hits))), nil
	default:
		return "", fmt.Errorf("prometheus: output must be value, sum, min, max, avg, count, or label")
	}
}

func promHits(text string, pattern promPattern) ([]promHit, error) {
	lines := 0
	var hits []promHit
	for _, line := range strings.Split(text, "\n") {
		lines++
		if lines > maxPromLines {
			return nil, fmt.Errorf("prometheus: input too large")
		}
		line = strings.TrimRight(line, "\r")
		if len(line) > maxPromLine {
			return nil, fmt.Errorf("prometheus: line too long")
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		name, labels, value, ok := parsePromSample(trimmed)
		if !ok || name != pattern.name || !pattern.matches(labels) {
			continue
		}
		if len(hits) >= maxPromMatches {
			return nil, fmt.Errorf("prometheus: too many matches")
		}
		hits = append(hits, promHit{labels: labels, value: value})
	}
	return hits, nil
}

func (p promPattern) matches(labels map[string]string) bool {
	for _, matcher := range p.matchers {
		value, ok := labels[matcher.key]
		switch matcher.op {
		case "=":
			if !ok || value != matcher.value {
				return false
			}
		case "!=":
			if ok && value == matcher.value {
				return false
			}
		case "=~":
			if !ok || !matcher.re.MatchString(value) {
				return false
			}
		case "!~":
			if ok && matcher.re.MatchString(value) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func parsePromPattern(raw string) (promPattern, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxPromPattern {
		return promPattern{}, fmt.Errorf("prometheus: pattern must be 1 to 256 bytes")
	}
	name, i, ok := scanPromName(raw, 0, true)
	if !ok {
		return promPattern{}, fmt.Errorf("prometheus: bad metric name")
	}
	var matchers []promMatcher
	if i < len(raw) && raw[i] == '{' {
		i++
		for {
			i = skipPromSpace(raw, i)
			if i < len(raw) && raw[i] == '}' {
				i++
				break
			}
			if len(matchers) >= maxPromLabels {
				return promPattern{}, fmt.Errorf("prometheus: at most 8 label matchers")
			}
			key, next, ok := scanPromName(raw, i, false)
			if !ok {
				return promPattern{}, fmt.Errorf("prometheus: bad label")
			}
			i = next
			op, next, ok := scanPromOp(raw, i)
			if !ok {
				return promPattern{}, fmt.Errorf("prometheus: bad label operator")
			}
			i = next
			if i >= len(raw) || raw[i] != '"' {
				return promPattern{}, fmt.Errorf("prometheus: label value must be quoted")
			}
			value, next, ok := scanPromQuoted(raw, i)
			if !ok {
				return promPattern{}, fmt.Errorf("prometheus: label value must be quoted")
			}
			matcher := promMatcher{key: key, op: op, value: value}
			if op == "=~" || op == "!~" {
				re, err := regexp.Compile(value)
				if err != nil {
					return promPattern{}, fmt.Errorf("prometheus: %w", err)
				}
				matcher.re = re
			}
			matchers = append(matchers, matcher)
			i = skipPromSpace(raw, next)
			if i < len(raw) && raw[i] == ',' {
				i++
				continue
			}
			if i < len(raw) && raw[i] == '}' {
				i++
				break
			}
			return promPattern{}, fmt.Errorf("prometheus: bad label list")
		}
	}
	if i != len(raw) {
		return promPattern{}, fmt.Errorf("prometheus: trailing %q", raw[i:])
	}
	return promPattern{name: name, matchers: matchers}, nil
}

func parsePromSample(line string) (string, map[string]string, float64, bool) {
	name, i, ok := scanPromName(line, 0, true)
	if !ok {
		return "", nil, 0, false
	}
	labels := map[string]string{}
	if i < len(line) && line[i] == '{' {
		labels, i, ok = scanPromLabels(line, i)
		if !ok {
			return "", nil, 0, false
		}
	}
	i = skipPromSpace(line, i)
	end := i
	for end < len(line) && line[end] != ' ' && line[end] != '\t' {
		end++
	}
	if end == i {
		return "", nil, 0, false
	}
	value, err := strconv.ParseFloat(line[i:end], 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return "", nil, 0, false
	}
	return name, labels, value, true
}

func scanPromLabels(line string, i int) (map[string]string, int, bool) {
	if i >= len(line) || line[i] != '{' {
		return nil, i, false
	}
	i++
	labels := map[string]string{}
	for {
		i = skipPromSpace(line, i)
		if i < len(line) && line[i] == '}' {
			return labels, i + 1, true
		}
		if len(labels) >= 32 {
			return nil, i, false
		}
		key, next, ok := scanPromName(line, i, false)
		if !ok || next >= len(line) || line[next] != '=' {
			return nil, i, false
		}
		value, next, ok := scanPromQuoted(line, next+1)
		if !ok {
			return nil, i, false
		}
		labels[key] = value
		i = skipPromSpace(line, next)
		if i < len(line) && line[i] == ',' {
			i++
			continue
		}
		if i < len(line) && line[i] == '}' {
			return labels, i + 1, true
		}
		return nil, i, false
	}
}

func scanPromName(s string, i int, metric bool) (string, int, bool) {
	if i >= len(s) {
		return "", i, false
	}
	start := i
	for i < len(s) {
		r := rune(s[i])
		if i == start {
			if !unicode.IsLetter(r) && r != '_' && !(metric && r == ':') {
				return "", start, false
			}
		} else if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && !(metric && r == ':') {
			break
		}
		i++
	}
	if i == start {
		return "", start, false
	}
	return s[start:i], i, true
}

func scanPromOp(s string, i int) (string, int, bool) {
	for _, op := range []string{"!~", "!=", "=~", "="} {
		if strings.HasPrefix(s[i:], op) {
			return op, i + len(op), true
		}
	}
	return "", i, false
}

func scanPromQuoted(s string, i int) (string, int, bool) {
	if i >= len(s) || s[i] != '"' {
		return "", i, false
	}
	i++
	var b strings.Builder
	for i < len(s) {
		c := s[i]
		i++
		if c == '"' {
			return b.String(), i, true
		}
		if c == '\\' {
			if i >= len(s) {
				return "", i, false
			}
			n := s[i]
			i++
			if n == 'n' {
				b.WriteByte('\n')
			} else {
				b.WriteByte(n)
			}
			continue
		}
		b.WriteByte(c)
	}
	return "", i, false
}

func skipPromSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

func validPromLabel(s string) bool {
	_, i, ok := scanPromName(s, 0, false)
	return ok && i == len(s)
}

func formatProm(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
