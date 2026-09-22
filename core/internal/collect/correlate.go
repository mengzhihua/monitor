package collect

import (
	"math"
	"sort"
	"strings"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

// Correlate ranks charts by how much they changed between a baseline window
// and a highlighted window (Netdata Metric Correlations: ks2 / volume).
func Correlate(reg *registry.Registry, db tsdb.Reader, method string, after, before, baseAfter, baseBefore int64) []Weight {
	return CorrelateGrouped(reg, db, method, after, before, baseAfter, baseBefore, "chart", 50)
}

// CorrelateGrouped ranks charts, contexts, or dimensions (group = chart|context|dimension).
func CorrelateGrouped(reg *registry.Registry, db tsdb.Reader, method string, after, before, baseAfter, baseBefore int64, group string, top int) []Weight {
	if before <= after {
		before = after + 1
	}
	if baseBefore <= baseAfter {
		baseBefore = baseAfter + 1
	}
	if top <= 0 || top > 200 {
		top = 50
	}
	group = strings.ToLower(group)
	type row struct {
		chart, ctx, title, dim string
		score, rate            float64
	}
	var rows []row
	for _, ch := range reg.Charts() {
		if ch == nil || strings.HasPrefix(ch.ID, "anomaly_detection.") {
			continue
		}
		title := ch.Title
		if title == "" {
			title = ch.ID
		}
		ctx := ch.Context
		if ctx == "" {
			ctx = ch.ID
		}
		var best float64
		var bestRate float64
		for _, d := range ch.Dims() {
			win := seriesValues(db, registry.SeriesID(ch.ID, d.ID), after, before)
			base := seriesValues(db, registry.SeriesID(ch.ID, d.ID), baseAfter, baseBefore)
			if len(win) < 5 || len(base) < 5 {
				continue
			}
			var score float64
			switch method {
			case "ks2":
				score = ks2(base, win)
			default:
				score = volumeShift(base, win)
			}
			if score == 0 {
				continue
			}
			rate := meanAbs(win)
			if group == "dimension" {
				rows = append(rows, row{chart: ch.ID, ctx: ctx, title: title, dim: d.ID, score: score, rate: rate})
			}
			if score > best {
				best, bestRate = score, rate
			}
		}
		if group != "dimension" && best > 0 {
			rows = append(rows, row{chart: ch.ID, ctx: ctx, title: title, score: best, rate: bestRate})
		}
	}
	if group == "context" {
		by := map[string]row{}
		for _, r := range rows {
			cur, ok := by[r.ctx]
			if !ok || r.score > cur.score {
				by[r.ctx] = r
			}
		}
		rows = rows[:0]
		for _, r := range by {
			rows = append(rows, r)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].score > rows[j].score })
	if len(rows) > top {
		rows = rows[:top]
	}
	out := make([]Weight, 0, len(rows))
	for _, r := range rows {
		out = append(out, Weight{Chart: r.chart, Context: r.ctx, Title: r.title, Dimension: r.dim, Score: r.score, AnomalyRate: r.rate})
	}
	return out
}

func seriesValues(db tsdb.Reader, id string, after, before int64) []float64 {
	bs, err := db.QueryTier(id, 0, after, before)
	if err != nil {
		return nil
	}
	out := make([]float64, 0, len(bs))
	for _, b := range bs {
		if b.Count == 0 {
			continue
		}
		v := b.Last
		if math.IsNaN(v) || math.IsInf(v, 0) {
			v = b.Avg()
		}
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		out = append(out, v)
	}
	return out
}

func volumeShift(base, win []float64) float64 {
	mb := meanOf(base)
	mw := meanOf(win)
	den := math.Abs(mb) + 1e-9
	return math.Abs(mw-mb) / den
}

func meanOf(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func meanAbs(xs []float64) float64 { return math.Abs(meanOf(xs)) }

// ks2 is the two-sample Kolmogorov–Smirnov statistic (sup |F1-F2|).
func ks2(a, b []float64) float64 {
	as := append([]float64(nil), a...)
	bs := append([]float64(nil), b...)
	sort.Float64s(as)
	sort.Float64s(bs)
	i, j := 0, 0
	na, nb := float64(len(as)), float64(len(bs))
	var d, fa, fb float64
	for i < len(as) || j < len(bs) {
		switch {
		case j == len(bs):
			i++
			fa = float64(i) / na
		case i == len(as):
			j++
			fb = float64(j) / nb
		case as[i] < bs[j]:
			i++
			fa = float64(i) / na
		case bs[j] < as[i]:
			j++
			fb = float64(j) / nb
		default:
			v := as[i]
			for i < len(as) && as[i] == v {
				i++
			}
			for j < len(bs) && bs[j] == v {
				j++
			}
			fa, fb = float64(i)/na, float64(j)/nb
		}
		if diff := math.Abs(fa - fb); diff > d {
			d = diff
		}
	}
	return d
}
