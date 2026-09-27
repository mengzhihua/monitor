package registry

import "sort"

// ChartsByContext returns matching charts sorted by priority then ID, like
// Charts. Filtering under the read lock avoids copying and sorting unrelated
// charts for callers that only need one metric context.
func (r *Registry) ChartsByContext(context string) []*Chart {
	r.mu.RLock()
	out := []*Chart{}
	for _, c := range r.charts {
		if c.Context == context {
			out = append(out, c)
		}
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].ID < out[j].ID
	})
	return out
}
