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

// grid splits [after, before] into `points` buckets aligned to the step.
func grid(after, before int64, points int) Result {
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
	after = after - (after % step)
	n := int((before - after + step - 1) / step)
	times := make([]int64, n)
	for i := range times {
		times[i] = after + int64(i+1)*step
	}
	return Result{After: after, Before: before, Step: step, Times: times}
}

// fold accumulates one series straight into the output grid.
type fold struct {
	fn     GroupFunc
	after  int64
	before int64
	step   int64
	n      int
	last   int64
	out    []float64
	seen   []bool
	counts []int64
	rawMed [][]float64
	bukMed [][]Bucket
}

func newFold(res Result, fn GroupFunc) *fold {
	f := &fold{after: res.After, before: res.Before, step: res.Step, n: len(res.Times)}
	if f.n > 0 {
		f.last = res.Times[f.n-1]
	}
	f.reset(fn)
	return f
}

func (f *fold) reset(fn GroupFunc) {
	f.fn = fn
	f.out = make([]float64, f.n)
	f.seen, f.counts, f.rawMed, f.bukMed = nil, nil, nil, nil
	if fn == GroupMedian {
		return
	}
	f.seen = make([]bool, f.n)
	if fn != GroupMin && fn != GroupMax && fn != GroupSum && fn != GroupLast {
		f.counts = make([]int64, f.n)
	}
}

func (f *fold) index(ts int64) (int, bool) {
	if f.n == 0 || ts <= f.after || ts > f.last {
		return 0, false
	}
	bi := int((ts - f.after - 1) / f.step)
	if bi < 0 || bi >= f.n {
		return 0, false
	}
	return bi, true
}

func (f *fold) addPoint(ts int64, v float64) {
	if f.fn == GroupMedian {
		bi, ok := f.index(ts)
		if !ok {
			return
		}
		if f.rawMed == nil {
			f.rawMed = make([][]float64, f.n)
		}
		f.rawMed[bi] = append(f.rawMed[bi], v)
		return
	}
	bi, ok := f.index(ts)
	if !ok {
		return
	}
	f.addValue(bi, v, v, v, v, 1)
}

func (f *fold) addBucket(b Bucket, every int64) {
	if every < 1 {
		every = 1
	}
	key := b.TS + every - 1
	if key > f.before && b.TS <= f.before {
		key = f.before
	}
	bi, ok := f.index(key)
	if !ok {
		return
	}
	if f.fn == GroupMedian {
		if f.bukMed == nil {
			f.bukMed = make([][]Bucket, f.n)
		}
		f.bukMed[bi] = append(f.bukMed[bi], b)
		return
	}
	f.addValue(bi, b.Min, b.Max, b.Sum, b.Last, b.Count)
}

func (f *fold) addValue(bi int, minV, maxV, sum, last float64, count int64) {
	if !f.seen[bi] {
		f.seen[bi] = true
		switch f.fn {
		case GroupMin:
			f.out[bi] = minV
		case GroupMax:
			f.out[bi] = maxV
		}
	}
	switch f.fn {
	case GroupMin:
		if minV < f.out[bi] {
			f.out[bi] = minV
		}
	case GroupMax:
		if maxV > f.out[bi] {
			f.out[bi] = maxV
		}
	case GroupLast:
		f.out[bi] = last
	case GroupSum:
		f.out[bi] += sum
	default:
		f.out[bi] += sum
		f.counts[bi] += count
	}
}

func (f *fold) finish() []float64 {
	out := f.out
	for i := range out {
		if f.fn == GroupMedian {
			if len(f.rawMed) > 0 {
				if len(f.rawMed[i]) == 0 {
					out[i] = math.NaN()
				} else {
					out[i] = reduce(f.rawMed[i], GroupMedian)
				}
			} else if len(f.bukMed) > 0 {
				if len(f.bukMed[i]) == 0 {
					out[i] = math.NaN()
				} else {
					out[i] = reduceBuckets(f.bukMed[i], GroupMedian)
				}
			} else {
				out[i] = math.NaN()
			}
			continue
		}
		if !f.seen[i] {
			out[i] = math.NaN()
		} else if f.counts != nil {
			if f.counts[i] == 0 {
				out[i] = math.NaN()
			} else {
				out[i] /= float64(f.counts[i])
			}
		}
	}
	return out
}

// Aggregate re-buckets raw points of several series onto a common grid.
// points must be equal for the grid: [after, before] split into `points`
// buckets of `step` seconds (step = ceil(range/points), at least 1).
func Aggregate(seriesPoints [][]Point, after, before int64, points int, fn GroupFunc) Result {
	res := grid(after, before, points)
	res.Values = make([][]float64, len(seriesPoints))
	f := newFold(res, fn)
	for si, pts := range seriesPoints {
		if si > 0 {
			f.reset(fn)
		}
		for _, p := range pts {
			f.addPoint(p.TS, p.Value)
		}
		res.Values[si] = f.finish()
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
