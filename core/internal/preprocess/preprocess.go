// Package preprocess turns a raw item value into a number. Steps run in order
// and match the Zabbix preprocessing steps this agent implements: trim, left
// and right trim, replace, regex, JSONPath, a small XPath, multiplier, boolean
// match, must-match, must-not-match, hex, octal, in-range, discard-unchanged,
// change (delta), and Prometheus text to JSON. A step can set on_fail to
// discard, value, or error.
// JavaScript runs operator config in a time-limited interpreter that has no
// host, network, or filesystem API.
package preprocess

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"
	"unicode"
)

// Step is one preprocessing operation. Unused fields are ignored for that type.
type Step struct {
	Type        string   `yaml:"type" json:"type"`
	Pattern     string   `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	Group       int      `yaml:"group,omitempty" json:"group,omitempty"`
	Path        string   `yaml:"path,omitempty" json:"path,omitempty"`
	Factor      float64  `yaml:"factor,omitempty" json:"factor,omitempty"`
	Replacement string   `yaml:"replacement,omitempty" json:"replacement,omitempty"`
	Min         *float64 `yaml:"min,omitempty" json:"min,omitempty"`
	Max         *float64 `yaml:"max,omitempty" json:"max,omitempty"`
	Script      string   `yaml:"script,omitempty" json:"script,omitempty"`
	OnFail      string   `yaml:"on_fail,omitempty" json:"on_fail,omitempty"`
	FailValue   string   `yaml:"fail_value,omitempty" json:"fail_value,omitempty"`
}

// ErrUnchanged means discard_unchanged dropped a repeat of the stored value.
// Callers skip the sample and keep the previous stored value.
var ErrUnchanged = errors.New("preprocess: discard unchanged")

// ErrDiscard means on_fail discard dropped the sample.
var ErrDiscard = errors.New("preprocess: discard value")

// ErrNotSupported means not_supported matched and the item should show that error.
var ErrNotSupported = errors.New("preprocess: not supported")

// Apply runs steps against input. prev is the previous numeric sample for a
// change step; pass math.NaN when there is none. The returned prev is the
// value to store for the next change step.
func Apply(steps []Step, input string, prev float64) (float64, float64, error) {
	text := input
	value := math.NaN()
	have := false
	for _, st := range steps {
		var err error
		switch strings.ToLower(strings.TrimSpace(st.Type)) {
		case "trim":
			text = strings.TrimSpace(text)
		case "ltrim":
			text = trimSide(text, st.Pattern, true)
		case "rtrim":
			text = trimSide(text, st.Pattern, false)
		case "replace":
			if st.Pattern == "" {
				return 0, prev, fmt.Errorf("replace: pattern required")
			}
			text = strings.ReplaceAll(text, st.Pattern, st.Replacement)
		case "regex":
			text, err = applyRegex(st, text)
			var syn *syntax.Error
			if errors.As(err, &syn) {
				return 0, prev, err
			}
		case "jsonpath":
			text, err = applyJSON(st.Path, text)
		case "xpath":
			text, err = applyXML(st.Path, text)
		case "multiplier":
			value, err = parseNum(text)
			if err == nil {
				factor := st.Factor
				if factor == 0 {
					factor = 1
				}
				value *= factor
				text = strconv.FormatFloat(value, 'f', -1, 64)
				have = true
			}
		case "bool":
			re, e := regexp.Compile(st.Pattern)
			if e != nil {
				return 0, prev, e
			}
			if re.MatchString(text) {
				value = 1
			} else {
				value = 0
			}
			text = strconv.FormatFloat(value, 'f', -1, 64)
			have = true
		case "matches":
			if err = mustMatch(st.Pattern, text, true); err != nil {
				return failStep(st, prev, err)
			}
		case "not_match":
			if err = mustMatch(st.Pattern, text, false); err != nil {
				return failStep(st, prev, err)
			}
		case "hex", "octal":
			value, err = parseBase(text, st.Type)
			if err != nil {
				return 0, prev, err
			}
			text = strconv.FormatInt(int64(value), 10)
			have = true
		case "in_range":
			value, err = parseNum(text)
			if err != nil {
				return 0, prev, err
			}
			if st.Min == nil && st.Max == nil {
				return 0, prev, fmt.Errorf("in_range: min or max required")
			}
			if st.Min != nil && value < *st.Min || st.Max != nil && value > *st.Max {
				return failStep(st, prev, fmt.Errorf("in_range: %v outside bounds", value))
			}
			text = strconv.FormatFloat(value, 'f', -1, 64)
			have = true
		case "discard_unchanged":
			value, err = parseNum(text)
			if err != nil {
				return 0, prev, err
			}
			if !math.IsNaN(prev) && value == prev {
				return 0, prev, ErrUnchanged
			}
			prev = value
			text = strconv.FormatFloat(value, 'f', -1, 64)
			have = true
		case "prometheus_to_json":
			text, err = applyPromJSON(st, text)
			if err != nil {
				return failStep(st, prev, err)
			}
			have = false
		case "not_supported":
			if strings.TrimSpace(st.Pattern) == "" || len(st.Pattern) > 256 {
				return 0, prev, fmt.Errorf("not_supported: pattern must be 1 to 256 bytes")
			}
			re, e := regexp.Compile(st.Pattern)
			if e != nil {
				return 0, prev, e
			}
			if re.MatchString(text) {
				msg := re.FindString(text)
				if msg == "" {
					msg = strings.TrimSpace(text)
				}
				runes := []rune(msg)
				if len(runes) > 128 {
					msg = string(runes[:128])
				}
				return failStep(st, prev, fmt.Errorf("%w: %s", ErrNotSupported, msg))
			}
		case "javascript", "js":
			text, err = applyJS(st.Script, text)
			if err != nil {
				return 0, prev, err
			}
			have = false
		case "change":
			value, err = parseNum(text)
			if err != nil {
				return 0, prev, err
			}
			if math.IsNaN(prev) {
				return failStep(st, value, fmt.Errorf("change: no previous value"))
			}
			delta := value - prev
			return delta, value, nil
		default:
			return 0, prev, fmt.Errorf("unknown preprocess step %q", st.Type)
		}
		if err != nil {
			return failStep(st, prev, err)
		}
	}
	if !have {
		var err error
		value, err = parseNum(text)
		if err != nil {
			return failStep(Step{}, prev, err)
		}
	}
	return value, prev, nil
}

func failStep(st Step, prev float64, err error) (float64, float64, error) {
	if err == nil || errors.Is(err, ErrUnchanged) || errors.Is(err, ErrDiscard) {
		return 0, prev, err
	}
	switch strings.ToLower(strings.TrimSpace(st.OnFail)) {
	case "":
		return 0, prev, err
	case "discard":
		return 0, prev, ErrDiscard
	case "value":
		value, perr := parseNum(st.FailValue)
		if perr != nil {
			return 0, prev, fmt.Errorf("on_fail value: %w", perr)
		}
		return value, value, nil
	case "error":
		msg := strings.TrimSpace(st.FailValue)
		if msg == "" {
			return 0, prev, fmt.Errorf("on_fail error needs fail_value")
		}
		runes := []rune(msg)
		if len(runes) > 256 {
			msg = string(runes[:256])
		}
		return 0, prev, fmt.Errorf("%s", msg)
	default:
		return 0, prev, fmt.Errorf("on_fail must be discard, value, or error")
	}
}

func trimSide(text, pattern string, left bool) string {
	if pattern == "" {
		if left {
			return strings.TrimLeftFunc(text, unicode.IsSpace)
		}
		return strings.TrimRightFunc(text, unicode.IsSpace)
	}
	if left {
		return strings.TrimLeft(text, pattern)
	}
	return strings.TrimRight(text, pattern)
}

func mustMatch(pattern, text string, want bool) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}
	matched := re.MatchString(text)
	if want && !matched {
		return fmt.Errorf("matches: value does not match")
	}
	if !want && matched {
		return fmt.Errorf("not_match: value matches")
	}
	return nil
}

func parseBase(text, kind string) (float64, error) {
	text = strings.TrimSpace(text)
	base := 16
	if strings.EqualFold(kind, "octal") {
		base = 8
		if text == "0" {
			return 0, nil
		}
		text = strings.TrimPrefix(text, "0")
	} else {
		text = strings.TrimPrefix(strings.TrimPrefix(text, "0x"), "0X")
	}
	if text == "" {
		return 0, fmt.Errorf("%s: empty value", kind)
	}
	n, err := strconv.ParseInt(text, base, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", kind, err)
	}
	return float64(n), nil
}

func applyRegex(st Step, text string) (string, error) {
	re, err := regexp.Compile(st.Pattern)
	if err != nil {
		return "", err
	}
	m := re.FindStringSubmatch(text)
	if m == nil {
		return "", fmt.Errorf("regex: no match")
	}
	group := st.Group
	if group == 0 && len(m) > 1 {
		group = 1
	}
	if group < 0 || group >= len(m) {
		return "", fmt.Errorf("regex: group %d missing", group)
	}
	return m[group], nil
}

func applyJSON(path, text string) (string, error) {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "$")
	path = strings.TrimPrefix(path, ".")
	var cur any
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	if err := dec.Decode(&cur); err != nil {
		return "", fmt.Errorf("jsonpath: %w", err)
	}
	for _, part := range splitPath(path) {
		name, idx, indexed := splitIndex(part)
		if name != "" {
			obj, ok := cur.(map[string]any)
			if !ok {
				return "", fmt.Errorf("jsonpath: %s is not an object", name)
			}
			cur, ok = obj[name]
			if !ok {
				return "", fmt.Errorf("jsonpath: missing %s", name)
			}
		}
		if indexed {
			arr, ok := cur.([]any)
			if !ok || idx < 0 || idx >= len(arr) {
				return "", fmt.Errorf("jsonpath: index %d", idx)
			}
			cur = arr[idx]
		}
	}
	switch v := cur.(type) {
	case string:
		return v, nil
	case json.Number:
		return v.String(), nil
	case bool:
		if v {
			return "1", nil
		}
		return "0", nil
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
}

func splitPath(path string) []string {
	path = strings.ReplaceAll(path, "[", ".[")
	var out []string
	for _, p := range strings.Split(path, ".") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func splitIndex(part string) (string, int, bool) {
	if !strings.HasPrefix(part, "[") {
		return part, 0, false
	}
	n := strings.TrimSuffix(strings.TrimPrefix(part, "["), "]")
	i, err := strconv.Atoi(n)
	if err != nil {
		return "", -1, true
	}
	return "", i, true
}

type xmlFrame struct {
	name  string
	index int
}

func applyXML(path, text string) (string, error) {
	path = strings.TrimSpace(path)
	path = strings.Trim(path, "/")
	if path == "" {
		return "", fmt.Errorf("xpath: empty path")
	}
	var want []xmlFrame
	for _, part := range strings.Split(path, "/") {
		if part == "" {
			continue
		}
		name, index, has := cutIndex(part)
		if !has {
			index = 1
		}
		if index < 1 {
			return "", fmt.Errorf("xpath: bad index in %s", part)
		}
		want = append(want, xmlFrame{name: name, index: index})
	}
	dec := xml.NewDecoder(strings.NewReader(text))
	var stack []xmlFrame
	counts := []map[string]int{{}}
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			parent := counts[len(counts)-1]
			parent[t.Name.Local]++
			stack = append(stack, xmlFrame{name: t.Name.Local, index: parent[t.Name.Local]})
			counts = append(counts, map[string]int{})
			if xmlMatch(stack, want) {
				var inner string
				if err := dec.DecodeElement(&inner, &t); err != nil {
					return "", err
				}
				return strings.TrimSpace(inner), nil
			}
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
				counts = counts[:len(counts)-1]
			}
		}
	}
	return "", fmt.Errorf("xpath: %s not found", path)
}

func xmlMatch(stack, want []xmlFrame) bool {
	if len(stack) != len(want) {
		return false
	}
	for i := range want {
		if stack[i].name != want[i].name || stack[i].index != want[i].index {
			return false
		}
	}
	return true
}

func cutIndex(part string) (string, int, bool) {
	i := strings.IndexByte(part, '[')
	if i < 0 {
		return part, 0, false
	}
	n := strings.TrimSuffix(part[i+1:], "]")
	v, err := strconv.Atoi(n)
	if err != nil {
		return part[:i], -1, true
	}
	return part[:i], v, true
}

func parseNum(text string) (float64, error) {
	text = strings.TrimSpace(text)
	text = strings.Trim(text, `"`)
	v, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("not a number %q", text)
	}
	return v, nil
}
