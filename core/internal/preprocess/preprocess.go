// Package preprocess turns a raw item value into a number. Steps run in order
// and match the Zabbix preprocessing steps this agent implements: trim, regex,
// JSONPath, a small XPath, multiplier, boolean match, and change (delta).
package preprocess

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Step is one preprocessing operation. Unused fields are ignored for that type.
type Step struct {
	Type    string  `yaml:"type" json:"type"`
	Pattern string  `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	Group   int     `yaml:"group,omitempty" json:"group,omitempty"`
	Path    string  `yaml:"path,omitempty" json:"path,omitempty"`
	Factor  float64 `yaml:"factor,omitempty" json:"factor,omitempty"`
}

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
		case "regex":
			text, err = applyRegex(st, text)
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
		case "change":
			value, err = parseNum(text)
			if err != nil {
				return 0, prev, err
			}
			if math.IsNaN(prev) {
				return 0, value, fmt.Errorf("change: no previous value")
			}
			delta := value - prev
			return delta, value, nil
		default:
			return 0, prev, fmt.Errorf("unknown preprocess step %q", st.Type)
		}
		if err != nil {
			return 0, prev, err
		}
	}
	if !have {
		var err error
		value, err = parseNum(text)
		if err != nil {
			return 0, prev, err
		}
	}
	return value, prev, nil
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
