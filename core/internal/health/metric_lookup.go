package health

import (
	"math"
	"sort"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

// metricLookup reads one evaluation's data sources. It cannot mutate alarms,
// routing or lifecycle state, and runs without holding the engine state lock.
// The anomaly source is captured by Engine.lookup so replacement via SetAnomaly
// cannot mix sources within a single lookup.
type metricLookup struct {
	db      *tsdb.Store
	anomaly AnomalySource
}

// value runs the alarm's database query and reduces it to one number.
func (q metricLookup) value(c *registry.Chart, l *Lookup, now time.Time) float64 {
	before := now.Add(l.At).Unix()
	after := before - int64(l.After/time.Second)
	dims := c.Dims()
	selected := map[string]bool{}
	for _, d := range l.Dimensions {
		selected[d] = true
	}
	if l.Kind != "" {
		return q.kind(c, l, selected, after, before)
	}
	var sum, total float64
	var matched bool
	var minSum, maxSum float64
	for _, d := range dims {
		isSel := len(selected) == 0 || selected[d.ID] || selected[d.Name]
		// Only percentage needs unselected series for its denominator. Avoid
		// reading and reducing every dimension on wide charts otherwise.
		if !isSel && !l.Percentage {
			continue
		}
		var vals []float64
		var pts []tsdb.Point
		if l.AnomalyBit {
			if q.anomaly != nil {
				vals = q.anomaly.RatesBetween(c.ID, d.ID, after, before)
			}
		} else {
			var err error
			pts, err = q.db.Query(registry.SeriesID(c.ID, d.ID), after, before)
			if err != nil || len(pts) == 0 {
				continue
			}
			vals = make([]float64, 0, len(pts))
			for _, p := range pts {
				if !math.IsNaN(p.Value) {
					vals = append(vals, p.Value)
				}
			}
		}
		if len(vals) == 0 {
			continue
		}
		var v float64
		if !l.AnomalyBit && l.Method == tsdb.GroupSum {
			// Integrate this same query snapshot, including NaN timestamps:
			// they delimit intervals even though their values contribute zero.
			v = integrate(pts, int64(c.UpdateEvery))
		} else {
			v = reduce(vals, l.Method)
		}
		if l.AbsValue {
			v = math.Abs(v)
		}
		if isSel {
			matched = true
			sum += v
			if l.MinMax != "" {
				minSum += reduce(vals, tsdb.GroupMin)
				maxSum += reduce(vals, tsdb.GroupMax)
			}
		}
		total += v
	}
	if !matched {
		return math.NaN()
	}
	if l.MinMax != "" {
		return maxSum - minSum
	}
	if l.Percentage {
		if total == 0 {
			return math.NaN()
		}
		return sum * 100 / total
	}
	return sum
}

// kind evaluates the Zabbix-style functions (Lookup.Kind). All of them
// work on [after, before] over the selected dimensions; per-dimension results
// are summed unless noted.
func (q metricLookup) kind(c *registry.Chart, l *Lookup, selected map[string]bool, after, before int64) float64 {
	isSel := func(d *registry.Dimension) bool {
		return len(selected) == 0 || selected[d.ID] || selected[d.Name]
	}
	if l.Kind == "nodata" {
		// 1 when no non-NaN point exists for any selected dim; never NaN.
		for _, d := range c.Dims() {
			if !isSel(d) {
				continue
			}
			pts, err := q.db.Query(registry.SeriesID(c.ID, d.ID), after, before)
			if err != nil {
				continue
			}
			for _, p := range pts {
				if !math.IsNaN(p.Value) {
					return 0
				}
			}
		}
		return 1
	}
	if l.Kind == "trendavg" || l.Kind == "trendmin" || l.Kind == "trendmax" ||
		l.Kind == "trendsum" || l.Kind == "trendcount" {
		return q.trend(c, l, selected, after, before)
	}
	var sum float64
	matched := false
	if l.Kind == "timeleft" {
		sum = timeLeftSentinel
	}
	for _, d := range c.Dims() {
		if !isSel(d) {
			continue
		}
		pts, err := q.db.Query(registry.SeriesID(c.ID, d.ID), after, before)
		if err != nil {
			continue
		}
		if l.Kind == "count" {
			// count answers 0 for a selected dimension that simply has no
			// matching samples; only query failures keep it out.
			matched = true
		}
		vals := make([]tsdb.Point, 0, len(pts))
		for _, p := range pts {
			if !math.IsNaN(p.Value) {
				vals = append(vals, p)
			}
		}
		if len(vals) == 0 {
			continue
		}
		if l.Kind == "skewness" || l.Kind == "kurtosis" {
			order := 4
			if l.Kind == "skewness" {
				order = 3
			}
			v, ok := standardizedMoment(vals, order)
			if !ok {
				continue
			}
			matched = true
			sum += v
			continue
		}
		matched = true
		switch l.Kind {
		case "first":
			sum += vals[0].Value
		case "change":
			sum += vals[len(vals)-1].Value - vals[0].Value
		case "stddev":
			sum += popStddev(vals)
		case "percentile":
			sum += percentileRank(vals, l.Percentile)
		case "count":
			sum += float64(countMatching(vals, l.CountOp, l.CountVal))
		case "forecast":
			a, b, ok := linearFit(vals)
			if !ok {
				continue
			}
			// fit origin is the first sample's timestamp
			x := float64(before-vals[0].TS) + l.Horizon.Seconds()
			sum += a + b*x
		case "timeleft":
			a, b, ok := linearFit(vals)
			if !ok || math.Abs(b) < 1e-12 {
				continue // stays at the sentinel
			}
			rem := (l.Target - (a + b*float64(before-vals[0].TS))) / b
			if rem < 0 {
				continue // moving away from the target: stays at the sentinel
			}
			if rem < sum {
				sum = rem
			}
		}
	}
	if !matched {
		return math.NaN()
	}
	if l.AbsValue {
		return math.Abs(sum)
	}
	return sum
}

// timeLeftSentinel replaces the unbounded Zabbix "never" answer; evaluate()
// treats +Inf as undefined, so timeleft reports a very large finite value.
const timeLeftSentinel = 1e15

// percentileRank is Zabbix's nearest-rank percentile. Values are sorted
// ascending and the index is ceil(n*p/100)-1, clamped to the sample list.
// p is 0 to 100. An empty list returns NaN.
func percentileRank(pts []tsdb.Point, p float64) float64 {
	n := len(pts)
	if n == 0 {
		return math.NaN()
	}
	sorted := make([]float64, n)
	for i, pt := range pts {
		sorted[i] = pt.Value
	}
	sort.Float64s(sorted)
	if p <= 0 {
		return sorted[0]
	}
	index := int(math.Ceil(float64(n)*p/100)) - 1
	if index < 0 {
		index = 0
	}
	if index >= n {
		index = n - 1
	}
	return sorted[index]
}

func countMatching(pts []tsdb.Point, op string, v float64) int {
	var n int
	for _, p := range pts {
		ok := true
		switch op {
		case "gt":
			ok = p.Value > v
		case "ge":
			ok = p.Value >= v
		case "lt":
			ok = p.Value < v
		case "le":
			ok = p.Value <= v
		case "eq":
			ok = p.Value == v
		case "ne":
			ok = p.Value != v
		}
		if ok {
			n++
		}
	}
	return n
}

// standardizedMoment is the Pearson moment coefficient. order 3 is skewness
// (m3 / m2^1.5) and order 4 is raw kurtosis (m4 / m2^2), not the excess
// kurtosis that subtracts 3. Moments divide by the sample count. Zero
// variance, or fewer than two samples, is undefined.
func standardizedMoment(pts []tsdb.Point, order int) (float64, bool) {
	n := len(pts)
	if n < 2 {
		return 0, false
	}
	var mean float64
	for _, p := range pts {
		mean += p.Value
	}
	mean /= float64(n)
	var m2, mk float64
	for _, p := range pts {
		d := p.Value - mean
		m2 += d * d
		pow := d
		for i := 1; i < order; i++ {
			pow *= d
		}
		mk += pow
	}
	m2 /= float64(n)
	mk /= float64(n)
	if m2 == 0 {
		return 0, false
	}
	if order == 3 {
		return mk / (m2 * math.Sqrt(m2)), true
	}
	return mk / (m2 * m2), true
}

func popStddev(pts []tsdb.Point) float64 {
	var m float64
	for _, p := range pts {
		m += p.Value
	}
	m /= float64(len(pts))
	var v float64
	for _, p := range pts {
		d := p.Value - m
		v += d * d
	}
	return math.Sqrt(v / float64(len(pts)))
}

// linearFit is ordinary least squares of (seconds since window start, value).
func linearFit(pts []tsdb.Point) (a, b float64, ok bool) {
	if len(pts) < 2 {
		return 0, 0, false
	}
	x0 := float64(pts[0].TS)
	var sx, sy, sxx, sxy float64
	for _, p := range pts {
		x := float64(p.TS) - x0
		sx += x
		sy += p.Value
		sxx += x * x
		sxy += x * p.Value
	}
	n := float64(len(pts))
	den := n*sxx - sx*sx
	if math.Abs(den) < 1e-12 {
		return 0, 0, false
	}
	b = (n*sxy - sx*sy) / den
	a = (sy - b*sx) / n
	return a, b, true
}

// trend reduces rollup-tier buckets; empty coverage falls back to the
// raw tier-0 samples so fresh installs still answer.
func (q metricLookup) trend(c *registry.Chart, l *Lookup, selected map[string]bool, after, before int64) float64 {
	var sum float64
	matched := false
	for _, d := range c.Dims() {
		if len(selected) != 0 && !selected[d.ID] && !selected[d.Name] {
			continue
		}
		id := registry.SeriesID(c.ID, d.ID)
		// Pick the finest tier whose effective start reaches back to `after`
		// (tier0's start is max(first sample, retention cutoff); tier>=1's is
		// its first rollup bucket). When none reaches far enough — or the
		// covering tiers turn out empty for this window — fall back to the
		// tiers that reach furthest back. NaN only if no tier has data.
		nTiers := q.db.TierCount()
		var bs []tsdb.Bucket
		starts := make([]int64, nTiers)
		for tier := 0; tier < nTiers; tier++ {
			start, ok := q.db.TierFirst(id, tier)
			if !ok {
				starts[tier] = -1
				continue
			}
			starts[tier] = start
			if start > after {
				continue
			}
			b, err := q.db.QueryTier(id, tier, after, before)
			if err == nil && len(b) > 0 {
				bs = b
				break
			}
		}
		for i := 0; len(bs) == 0 && i < nTiers; i++ {
			// Take the candidate with the smallest start (reaches furthest
			// back) that still returns buckets for the window.
			best, bestStart := -1, int64(0)
			for tier := 0; tier < nTiers; tier++ {
				if starts[tier] >= 0 && (best < 0 || starts[tier] < bestStart) {
					best, bestStart = tier, starts[tier]
				}
			}
			if best < 0 {
				break
			}
			starts[best] = -1
			if b, err := q.db.QueryTier(id, best, after, before); err == nil {
				bs = b
			}
		}
		if len(bs) == 0 {
			continue
		}
		var v float64
		{
			switch l.Kind {
			case "trendavg":
				var s float64
				var n int64
				for _, b := range bs {
					s += b.Sum
					n += b.Count
				}
				if n == 0 {
					continue
				}
				v = s / float64(n)
			case "trendmin":
				v = bs[0].Min
				for _, b := range bs[1:] {
					if b.Min < v {
						v = b.Min
					}
				}
			case "trendmax":
				v = bs[0].Max
				for _, b := range bs[1:] {
					if b.Max > v {
						v = b.Max
					}
				}
			case "trendsum":
				for _, b := range bs {
					v += b.Sum
				}
			case "trendcount":
				for _, b := range bs {
					v += float64(b.Count)
				}
			}
		}
		matched = true
		sum += v
	}
	if !matched {
		return math.NaN()
	}
	return sum
}

// integrate sums per-second rates over the time they were in effect, so
// `sum` yields a total regardless of the collection interval. Each sample
// covers the gap since the previous one, capped at twice the chart interval
// (a longer gap means missed collections, not a sustained rate).
func integrate(pts []tsdb.Point, every int64) float64 {
	if every <= 0 {
		every = 1
	}
	var total float64
	prev := int64(-1)
	for _, p := range pts {
		dt := every
		if prev >= 0 {
			if d := p.TS - prev; d > 0 && d <= 2*every {
				dt = d
			}
		}
		prev = p.TS
		if !math.IsNaN(p.Value) {
			total += p.Value * float64(dt)
		}
	}
	return total
}

func reduce(vals []float64, fn tsdb.GroupFunc) float64 {
	res := tsdb.Aggregate([][]tsdb.Point{pointsOf(vals)}, 0, int64(len(vals)), 1, fn)
	if len(res.Values) == 0 || len(res.Values[0]) == 0 {
		return math.NaN()
	}
	return res.Values[0][0]
}

func pointsOf(vals []float64) []tsdb.Point {
	pts := make([]tsdb.Point, len(vals))
	for i, v := range vals {
		pts[i] = tsdb.Point{TS: int64(i + 1), Value: v}
	}
	return pts
}
