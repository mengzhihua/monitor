package preprocess

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxPromJSONBytes    = 256 * 1024
	maxPromJSONLines    = 20000
	maxPromJSONLine     = 8192
	maxPromJSONSamples  = 4096
	maxPromJSONPattern  = 256
	maxPromJSONMatchers = 8
)

type promJSONSample struct {
	Name    string            `json:"name"`
	Value   string            `json:"value"`
	Labels  map[string]string `json:"labels,omitempty"`
	Type    string            `json:"type,omitempty"`
	LineRaw string            `json:"line_raw"`
}

type promJSONMatcher struct {
	key, op, value string
	re             *regexp.Regexp
}

type promJSONPattern struct {
	name     string
	matchers []promJSONMatcher
	any      bool
}

func applyPromJSON(st Step, text string) (string, error) {
	if len(text) > maxPromJSONBytes {
		return "", fmt.Errorf("prometheus_to_json: input too large")
	}
	pattern, err := parsePromJSONPattern(st.Pattern)
	if err != nil {
		return "", err
	}
	samples, err := promJSONSamples(text, pattern)
	if err != nil {
		return "", err
	}
	if !pattern.any && len(samples) == 0 {
		return "", fmt.Errorf("prometheus_to_json: no match")
	}
	body, err := json.Marshal(samples)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func promJSONSamples(text string, pattern promJSONPattern) ([]promJSONSample, error) {
	types := map[string]string{}
	var out []promJSONSample
	lines := 0
	for _, line := range strings.Split(text, "\n") {
		lines++
		if lines > maxPromJSONLines {
			return nil, fmt.Errorf("prometheus_to_json: input too large")
		}
		line = strings.TrimRight(line, "\r")
		if len(line) > maxPromJSONLine {
			return nil, fmt.Errorf("prometheus_to_json: line too long")
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			if name, kind, ok := promJSONType(trimmed); ok {
				types[name] = kind
			}
			continue
		}
		name, labels, value, ok := parsePromJSONSample(trimmed)
		if !ok || !pattern.matches(name, labels) {
			continue
		}
		if len(out) >= maxPromJSONSamples {
			return nil, fmt.Errorf("prometheus_to_json: too many samples")
		}
		kind := types[name]
		if kind == "" {
			kind = "untyped"
		}
		rawLabels := labels
		if len(rawLabels) == 0 {
			rawLabels = nil
		}
		out = append(out, promJSONSample{
			Name: name, Value: formatPromJSON(value), Labels: rawLabels, Type: kind, LineRaw: trimmed,
		})
	}
	if out == nil {
		out = []promJSONSample{}
	}
	return out, nil
}

func (p promJSONPattern) matches(name string, labels map[string]string) bool {
	if p.any {
		return true
	}
	if name != p.name {
		return false
	}
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
			if !ok || matcher.re == nil || !matcher.re.MatchString(value) {
				return false
			}
		case "!~":
			if ok && matcher.re != nil && matcher.re.MatchString(value) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func promJSONType(line string) (string, string, bool) {
	fields := strings.Fields(line)
	if len(fields) < 4 || fields[1] != "TYPE" {
		return "", "", false
	}
	return fields[2], strings.ToLower(fields[3]), true
}

func parsePromJSONPattern(raw string) (promJSONPattern, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return promJSONPattern{any: true}, nil
	}
	if len(raw) > maxPromJSONPattern {
		return promJSONPattern{}, fmt.Errorf("prometheus_to_json: pattern must be at most 256 bytes")
	}
	name, i, ok := scanPromJSONName(raw, 0, true)
	if !ok {
		return promJSONPattern{}, fmt.Errorf("prometheus_to_json: bad metric name")
	}
	var matchers []promJSONMatcher
	if i < len(raw) && raw[i] == '{' {
		i++
		for {
			i = skipPromJSONSpace(raw, i)
			if i < len(raw) && raw[i] == '}' {
				i++
				break
			}
			if len(matchers) >= maxPromJSONMatchers {
				return promJSONPattern{}, fmt.Errorf("prometheus_to_json: at most 8 label matchers")
			}
			key, next, ok := scanPromJSONName(raw, i, false)
			if !ok {
				return promJSONPattern{}, fmt.Errorf("prometheus_to_json: bad label")
			}
			op, next, ok := scanPromJSONOp(raw, next)
			if !ok || next >= len(raw) || raw[next] != '"' {
				return promJSONPattern{}, fmt.Errorf("prometheus_to_json: bad label operator")
			}
			value, next, ok := scanPromJSONQuoted(raw, next)
			if !ok {
				return promJSONPattern{}, fmt.Errorf("prometheus_to_json: label value must be quoted")
			}
			matcher := promJSONMatcher{key: key, op: op, value: value}
			if op == "=~" || op == "!~" {
				re, err := regexp.Compile(value)
				if err != nil {
					return promJSONPattern{}, fmt.Errorf("prometheus_to_json: %w", err)
				}
				matcher.re = re
			}
			matchers = append(matchers, matcher)
			i = skipPromJSONSpace(raw, next)
			if i < len(raw) && raw[i] == ',' {
				i++
				continue
			}
			if i < len(raw) && raw[i] == '}' {
				i++
				break
			}
			return promJSONPattern{}, fmt.Errorf("prometheus_to_json: bad label list")
		}
	}
	if i != len(raw) {
		return promJSONPattern{}, fmt.Errorf("prometheus_to_json: trailing %q", raw[i:])
	}
	return promJSONPattern{name: name, matchers: matchers}, nil
}

func parsePromJSONSample(line string) (string, map[string]string, float64, bool) {
	name, i, ok := scanPromJSONName(line, 0, true)
	if !ok {
		return "", nil, 0, false
	}
	labels := map[string]string{}
	if i < len(line) && line[i] == '{' {
		labels, i, ok = scanPromJSONLabels(line, i)
		if !ok {
			return "", nil, 0, false
		}
	}
	i = skipPromJSONSpace(line, i)
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

func scanPromJSONLabels(line string, i int) (map[string]string, int, bool) {
	i++
	labels := map[string]string{}
	for {
		i = skipPromJSONSpace(line, i)
		if i < len(line) && line[i] == '}' {
			return labels, i + 1, true
		}
		if len(labels) >= 32 {
			return nil, i, false
		}
		key, next, ok := scanPromJSONName(line, i, false)
		if !ok || next >= len(line) || line[next] != '=' {
			return nil, i, false
		}
		value, next, ok := scanPromJSONQuoted(line, next+1)
		if !ok {
			return nil, i, false
		}
		labels[key] = value
		i = skipPromJSONSpace(line, next)
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

func scanPromJSONName(s string, i int, metric bool) (string, int, bool) {
	if i >= len(s) {
		return "", i, false
	}
	start := i
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if i == start {
			if !unicode.IsLetter(r) && r != '_' && !(metric && r == ':') {
				return "", start, false
			}
		} else if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && !(metric && r == ':') {
			break
		}
		i += size
	}
	if i == start {
		return "", start, false
	}
	return s[start:i], i, true
}

func scanPromJSONOp(s string, i int) (string, int, bool) {
	for _, op := range []string{"!~", "!=", "=~", "="} {
		if strings.HasPrefix(s[i:], op) {
			return op, i + len(op), true
		}
	}
	return "", i, false
}

func scanPromJSONQuoted(s string, i int) (string, int, bool) {
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

func skipPromJSONSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

func formatPromJSON(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
