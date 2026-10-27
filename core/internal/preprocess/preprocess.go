// Package preprocess turns a raw item value into a number. Steps run in order
// and match the Zabbix preprocessing steps this agent implements: trim, left
// and right trim, replace, regex, JSONPath, a small XPath, multiplier, boolean
// match, must-match, must-not-match, hex, octal, in-range, discard-unchanged,
// change (delta), payload error checks, and Prometheus text to JSON.
// error_json, error_xml, and error_regex fail the sample when a payload
// reports an error and otherwise leave the text unchanged.
// A step can set on_fail to discard, value, or error.
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
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Step is one preprocessing operation. Unused fields are ignored for that type.
type Step struct {
	Type        string            `yaml:"type" json:"type"`
	Pattern     string            `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	Group       int               `yaml:"group,omitempty" json:"group,omitempty"`
	Path        string            `yaml:"path,omitempty" json:"path,omitempty"`
	Factor      float64           `yaml:"factor,omitempty" json:"factor,omitempty"`
	Replacement string            `yaml:"replacement,omitempty" json:"replacement,omitempty"`
	Min         *float64          `yaml:"min,omitempty" json:"min,omitempty"`
	Max         *float64          `yaml:"max,omitempty" json:"max,omitempty"`
	Map         map[string]string `yaml:"map,omitempty" json:"map,omitempty"`
	Seconds     int               `yaml:"seconds,omitempty" json:"seconds,omitempty"`
	Script      string            `yaml:"script,omitempty" json:"script,omitempty"`
	OnFail      string            `yaml:"on_fail,omitempty" json:"on_fail,omitempty"`
	FailValue   string            `yaml:"fail_value,omitempty" json:"fail_value,omitempty"`
	Delimiter   string            `yaml:"delimiter,omitempty" json:"delimiter,omitempty"`
	Qualifier   string            `yaml:"qualifier,omitempty" json:"qualifier,omitempty"`
	Header      bool              `yaml:"header,omitempty" json:"header,omitempty"`
	NoQuote     bool              `yaml:"no_quote,omitempty" json:"no_quote,omitempty"`
	Fields      []WalkField       `yaml:"fields,omitempty" json:"fields,omitempty"`
}

// WalkField is one column of an snmp_walk_to_json step. Name is a {#MACRO}.
type WalkField struct {
	Name string `yaml:"name" json:"name"`
	OID  string `yaml:"oid" json:"oid"`
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
// value to store for the next change step. Timed steps need ApplyAt from collectors.
func Apply(steps []Step, input string, prev float64) (float64, float64, error) {
	value, mem, err := ApplyAt(steps, input, Memory{Value: prev}, time.Time{})
	return value, mem.Value, err
}

func failStep(st Step, mem Memory, err error) (float64, Memory, error) {
	if err == nil || errors.Is(err, ErrUnchanged) || errors.Is(err, ErrDiscard) || errors.Is(err, ErrThrottled) || errors.Is(err, ErrNoBaseline) {
		return 0, mem, err
	}
	switch strings.ToLower(strings.TrimSpace(st.OnFail)) {
	case "":
		return 0, mem, err
	case "discard":
		return 0, mem, ErrDiscard
	case "value":
		value, perr := parseNum(st.FailValue)
		if perr != nil {
			return 0, mem, fmt.Errorf("on_fail value: %w", perr)
		}
		mem.Value = value
		return value, mem, nil
	case "error":
		msg := strings.TrimSpace(st.FailValue)
		if msg == "" {
			return 0, mem, fmt.Errorf("on_fail error needs fail_value")
		}
		runes := []rune(msg)
		if len(runes) > 256 {
			msg = string(runes[:256])
		}
		return 0, mem, fmt.Errorf("%s", msg)
	default:
		return 0, mem, fmt.Errorf("on_fail must be discard, value, or error")
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

func payloadError(st Step, text string) (string, bool, error) {
	switch strings.ToLower(strings.TrimSpace(st.Type)) {
	case "error_json":
		if strings.TrimSpace(st.Path) == "" {
			return "", false, fmt.Errorf("error_json: path required")
		}
		value, err := applyJSON(st.Path, text)
		if err != nil {
			if jsonPathAbsent(err) {
				return "", true, nil
			}
			return "", false, err
		}
		if strings.TrimSpace(value) == "" {
			return "", true, nil
		}
		return value, false, nil
	case "error_xml":
		if strings.TrimSpace(st.Path) == "" {
			return "", false, fmt.Errorf("error_xml: path required")
		}
		value, err := applyXML(st.Path, text)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				return "", true, nil
			}
			return "", false, err
		}
		if strings.TrimSpace(value) == "" {
			return "", true, nil
		}
		return value, false, nil
	case "error_regex":
		if st.Pattern == "" {
			return "", false, fmt.Errorf("error_regex: pattern required")
		}
		re, err := regexp.Compile(st.Pattern)
		if err != nil {
			return "", false, err
		}
		match := re.FindStringSubmatch(text)
		if match == nil {
			return "", true, nil
		}
		if len(match) > 1 && strings.TrimSpace(match[1]) != "" {
			return match[1], false, nil
		}
		return match[0], false, nil
	default:
		return "", false, fmt.Errorf("unknown preprocess step %q", st.Type)
	}
}

func jsonPathAbsent(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "missing") || strings.Contains(msg, "index")
}

func clipStepError(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) > 256 {
		return string(runes[:256])
	}
	return s
}
