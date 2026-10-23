package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/mengzhihua/monitor/core/internal/collect"
)

func (s *Server) handleWeights(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	method := q.Get("method")
	if method == "" {
		method = "anomaly-rate"
	}
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	if method == "ks2" || method == "volume" {
		now := time.Now().Unix()
		before := parseTime(q.Get("before"), now, now)
		after := parseTime(q.Get("after"), before, before-300)
		baseBefore := parseTime(q.Get("baseline_before"), before, after)
		baseAfterSrc := q.Get("baseline_after")
		if baseAfterSrc == "" {
			baseAfterSrc = q.Get("baseline")
		}
		baseAfter := parseTime(baseAfterSrc, baseBefore, baseBefore-600)
		group := q.Get("group")
		if group == "" {
			group = "chart"
		}
		top, _ := strconv.Atoi(q.Get("top"))
		weights := collect.CorrelateGrouped(v.reg, v.db, method, after, before, baseAfter, baseBefore, group, top)
		writeJSON(w, map[string]any{"method": method, "group": group, "weights": weights, "count": len(weights),
			"after": after, "before": before, "baseline_after": baseAfter, "baseline_before": baseBefore})
		return
	}
	c := s.sched.Collector("ml")
	wp, ok := c.(collect.WeightProvider)
	if !ok || c == nil {
		http.Error(w, "ml collector disabled", http.StatusNotFound)
		return
	}
	weights := wp.Weights(method)
	top, _ := strconv.Atoi(q.Get("top"))
	if top > 0 && top < len(weights) {
		weights = weights[:top]
	}
	writeJSON(w, map[string]any{"method": method, "group": "chart", "weights": weights, "count": len(weights)})
}
