package api

import (
	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

// applyDataAnomalies adds current metadata and optionally replaces values with
// anomaly history. Metric-only requests retain their original time series.
func (s *Server) applyDataAnomalies(dims []dataDimension, series *dataSeries, bit bool) []float64 {
	src := s.anomaly()
	anom := make([]float64, len(dims))
	if src != nil {
		for i, d := range dims {
			var sum, n float64
			for _, c := range d.charts {
				if r, ok := src.Rate(c.ID, d.id); ok {
					sum += r
					n++
				}
			}
			if n > 0 {
				anom[i] = sum / n
			}
		}
	}
	if flags := s.anomalies(); flags != nil {
		for i, d := range dims {
			if anom[i] != 0 {
				continue
			}
			for _, c := range d.charts {
				if flags[registry.SeriesID(c.ID, d.id)] {
					anom[i] = 100
					break
				}
			}
		}
	}
	if bit {
		for di, d := range dims {
			for i, t := range series.times {
				end := t
				start := t
				if series.step > 0 {
					start = t - series.step
				}
				if i > 0 {
					start = series.times[i-1]
				}
				var rates []float64
				if src != nil {
					for _, c := range d.charts {
						rates = append(rates, src.RatesBetween(c.ID, d.id, start, end)...)
					}
				}
				if i >= len(series.values[di]) {
					continue
				}
				if len(rates) == 0 {
					series.values[di][i] = 0
					continue
				}
				s := 0.0
				for _, r := range rates {
					s += r
				}
				series.values[di][i] = s / float64(len(rates))
			}
		}
	}

	return anom
}

func (s *Server) anomaly() health.AnomalySource {
	if s.opt.Anomaly != nil {
		return s.opt.Anomaly
	}
	if s.sched != nil {
		if c := s.sched.Collector("ml"); c != nil {
			if src, ok := c.(health.AnomalySource); ok {
				return src
			}
		}
	}
	return nil
}

func (s *Server) anomalies() map[string]bool {
	c := s.sched.Collector("ml")
	if p, ok := c.(collect.AnomalyProvider); ok && p != nil {
		return p.DimAnomalies()
	}
	return nil
}
