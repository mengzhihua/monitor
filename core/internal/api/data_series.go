package api

import (
	"math"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

type dataSeries struct {
	times               []int64
	values              [][]float64
	step, after, before int64
}

func readDataSeries(db tsdb.Reader, dims []dataDimension, query dataQuery, tier int) (dataSeries, error) {
	values := make([][]float64, 0, len(dims))
	var times []int64
	var step, resAfter, resBefore int64

	for _, d := range dims {
		if query.anomalyBit {
			// Anomaly history replaces every metric value below. Reuse the
			// TSDB grid without reading or decoding those discarded series.
			if times == nil {
				agg := tsdb.QueryGrid(query.after, query.before, query.points)
				times = agg.Times
				step, resAfter, resBefore = agg.Step, agg.After, agg.Before
			}
			values = append(values, make([]float64, len(times)))
		} else {
			parts := make([][]float64, 0, len(d.charts))
			for _, c := range d.charts {
				agg, err := db.QueryAggregated(registry.SeriesID(c.ID, d.id), tier, query.after, query.before, query.points, query.group)
				if err != nil {
					return dataSeries{}, err
				}
				if times == nil {
					times = agg.Times
					step, resAfter, resBefore = agg.Step, agg.After, agg.Before
				}
				if len(agg.Values) == 0 {
					parts = append(parts, nil)
					continue
				}
				parts = append(parts, agg.Values[0])
			}
			values = append(values, sumAligned(parts))
		}
	}

	return dataSeries{times: times, values: values, step: step, after: resAfter, before: resBefore}, nil
}

// sumAligned adds same-index values across series (NaN skipped). One series is a no-op.
func sumAligned(parts [][]float64) []float64 {
	if len(parts) == 0 {
		return nil
	}
	if len(parts) == 1 {
		return parts[0]
	}
	n := len(parts[0])
	out := make([]float64, n)
	for i := range out {
		sum, ok := 0.0, false
		for _, p := range parts {
			if i < len(p) && !math.IsNaN(p[i]) {
				sum += p[i]
				ok = true
			}
		}
		if ok {
			out[i] = sum
		} else {
			out[i] = math.NaN()
		}
	}
	return out
}
