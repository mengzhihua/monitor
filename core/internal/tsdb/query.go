package tsdb

import (
	"math"
	"sort"
)

type GroupFunc string

const (
	GroupAverage GroupFunc = "average"
	GroupMin     GroupFunc = "min"
	GroupMax     GroupFunc = "max"
	GroupSum     GroupFunc = "sum"
	GroupMedian  GroupFunc = "median"
	GroupLast    GroupFunc = "last"
)

func ParseGroup(s string) GroupFunc {
	switch GroupFunc(s) {
	case GroupMin, GroupMax, GroupSum, GroupMedian, GroupLast:
		return GroupFunc(s)
	case "avg", "average", "mean", "":
		return GroupAverage
	}
	return GroupAverage
}

// Result is a time-aligned matrix: Times[i] is the bucket end timestamp and
// Values[s][i] the aggregated value of series s in that bucket (NaN = no data).
type Result struct {
	After  int64
	Before int64
	Step   int64
	Times  []int64
	Values [][]float64
}

// Aggregate re-buckets raw points of several series onto a common grid.
// points must be equal for the grid: [after, before] split into `points`
// buckets of `step` seconds (step = ceil(range/points), at least 1).
func Aggregate(seriesPoints [][]Point, after, before int64, points int, fn GroupFunc) Result {
	if before <= after {
		before = after + 1
	}
	span := before - after
	if points <= 0 {
		points = int(span)
	}
	step := (span + int64(points) - 1) / int64(points)
	if step < 1 {
		step = 1
	}
	// align grid to step boundaries
	after = after - (after % step)
	n := int((before - after + step - 1) / step)
	times := make([]int64, n)
	for i := range times {
		times[i] = after + int64(i+1)*step
	}
	res := Result{After: after, Before: before, Step: step, Times: times, Values: make([][]float64, len(seriesPoints))}
	for si, pts := range seriesPoints {
		out := make([]float64, n)
		var seen []bool
		var counts []int64
		if fn != GroupMedian && fn != GroupMin && fn != GroupMax && fn != GroupSum && fn != GroupLast {
			counts = make([]int64, n)
		}
		var buckets [][]float64
		if fn == GroupMedian {
			buckets = make([][]float64, n)
		} else {
			seen = make([]bool, n)
		}
		for _, p := range pts {
			if p.TS <= after || p.TS > times[n-1] {
				continue
			}
			bi := int((p.TS - after - 1) / step)
			if bi < 0 || bi >= n {
				continue
			}
			if fn == GroupMedian {
				buckets[bi] = append(buckets[bi], p.Value)
				continue
			}
			if !seen[bi] {
				seen[bi] = true
				switch fn {
				case GroupMin, GroupMax:
					out[bi] = p.Value
				}
			}
			switch fn {
			case GroupMin:
				if p.Value < out[bi] {
					out[bi] = p.Value
				}
			case GroupMax:
				if p.Value > out[bi] {
					out[bi] = p.Value
				}
			case GroupLast:
				out[bi] = p.Value
			case GroupSum:
				out[bi] += p.Value
			default:
				out[bi] += p.Value
				counts[bi]++
			}
		}
		for i := range out {
			if fn == GroupMedian {
				if len(buckets[i]) == 0 {
					out[i] = math.NaN()
				} else {
					out[i] = reduce(buckets[i], fn)
				}
				continue
			}
			if !seen[i] {
				out[i] = math.NaN()
			} else if counts != nil {
				out[i] /= float64(counts[i])
			}
		}
		res.Values[si] = out
	}
	return res
}

func reduce(v []float64, fn GroupFunc) float64 {
	switch fn {
	case GroupMin:
		m := v[0]
		for _, x := range v[1:] {
			if x < m {
				m = x
			}
		}
		return m
	case GroupMax:
		m := v[0]
		for _, x := range v[1:] {
			if x > m {
				m = x
			}
		}
		return m
	case GroupSum:
		var s float64
		for _, x := range v {
			s += x
		}
		return s
	case GroupMedian:
		c := append([]float64(nil), v...)
		sort.Float64s(c)
		if len(c)%2 == 1 {
			return c[len(c)/2]
		}
		return (c[len(c)/2-1] + c[len(c)/2]) / 2
	case GroupLast:
		return v[len(v)-1]
	default:
		var s float64
		for _, x := range v {
			s += x
		}
		return s / float64(len(v))
	}
}
