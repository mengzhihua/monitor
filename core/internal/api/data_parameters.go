package api

import (
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

// parseTime accepts unix seconds or a relative offset (negative = seconds
// before now / before `before`).
func parseTime(s string, rel int64, def int64) int64 {
	if s == "" {
		return def
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	if n <= 0 {
		return rel + n
	}
	return n
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

func parseGroupBy(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func hasGroup(flags []string, name string) bool {
	for _, f := range flags {
		if f == name {
			return true
		}
		if name == "dimension" && (f == "dimensions" || f == "dim") {
			return true
		}
	}
	return false
}

func wantsAnomalyBit(opts string) bool {
	opts = strings.ToLower(opts)
	return strings.Contains(opts, "anomaly-bit") || strings.Contains(opts, "anomaly_bit")
}

// dataQuery normalizes HTTP input once, before selecting or reading series.
// The caller supplies now so relative ranges can share a single time anchor.
type dataQuery struct {
	chart, context         string
	after, before          int64
	points                 int
	group                  tsdb.GroupFunc
	dimensions             map[string]bool
	showHidden, anomalyBit bool
	tier, groupBy          string
}

func parseDataQuery(q url.Values, now int64) dataQuery {
	before := parseTime(q.Get("before"), now, now)
	after := parseTime(q.Get("after"), before, before-600)
	if after >= before {
		after = before - 600
	}
	points, _ := strconv.Atoi(q.Get("points"))
	if points <= 0 {
		points = int(before - after)
	}
	if points > 10000 {
		points = 10000
	}
	group := tsdb.ParseGroup(q.Get("group"))
	want := map[string]bool{}
	if d := q.Get("dimensions"); d != "" {
		for _, x := range strings.Split(d, ",") {
			want[strings.TrimSpace(x)] = true
		}
	}

	opts := q.Get("options")
	return dataQuery{
		chart: q.Get("chart"), context: q.Get("context"),
		after: after, before: before, points: points, group: group,
		dimensions: want, showHidden: strings.Contains(opts, "all-dimensions"),
		anomalyBit: wantsAnomalyBit(opts), tier: q.Get("tier"),
		groupBy: strings.ToLower(strings.TrimSpace(q.Get("group_by"))),
	}
}
