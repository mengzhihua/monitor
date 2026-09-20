package health

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
)

//go:embed rules/*.yaml
var defaultRules embed.FS

// DefaultRules returns the rules shipped with the agent.
func DefaultRules() ([]*Rule, error) {
	entries, err := fs.ReadDir(defaultRules, "rules")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	var out []*Rule
	for _, n := range names {
		b, err := defaultRules.ReadFile("rules/" + n)
		if err != nil {
			return nil, err
		}
		rs, err := ParseRules(b, "builtin:"+n)
		if err != nil {
			return nil, fmt.Errorf("builtin rules: %w", err)
		}
		out = append(out, rs...)
	}
	return out, nil
}

// Merge overlays later rule sets on earlier ones: a rule with the same name
// replaces the previous definition (so health.d can override built-ins).
func Merge(sets ...[]*Rule) []*Rule {
	idx := map[string]int{}
	var out []*Rule
	for _, set := range sets {
		for _, r := range set {
			if i, ok := idx[r.Spec.Name]; ok {
				out[i] = r
				continue
			}
			idx[r.Spec.Name] = len(out)
			out = append(out, r)
		}
	}
	return out
}
