package api

import (
	"errors"
	"strconv"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

type dataDimension struct {
	id, name string
	charts   []*registry.Chart
}

func selectDataDimensions(charts []*registry.Chart, query dataQuery) []dataDimension {
	seen := map[string]int{}
	var dims []dataDimension
	for _, c := range charts {
		for _, d := range c.Dims() {
			if d.Hidden && !query.showHidden && len(query.dimensions) == 0 {
				continue
			}
			if len(query.dimensions) > 0 && !query.dimensions[d.ID] && !query.dimensions[d.Name] {
				continue
			}
			name := d.Name
			if name == "" {
				name = d.ID
			}
			if i, ok := seen[d.ID]; ok {
				dims[i].charts = append(dims[i].charts, c)
				continue
			}
			seen[d.ID] = len(dims)
			dims = append(dims, dataDimension{id: d.ID, name: name, charts: []*registry.Chart{c}})
		}
	}

	return dims
}

// planDataTier falls back to finer storage only for automatic selection.
// Explicit tiers must remain valid even when an anomaly query skips metric reads.
func planDataTier(db tsdb.Reader, query dataQuery, dims []dataDimension) (int, error) {
	var seriesIDs []string
	for _, d := range dims {
		for _, c := range d.charts {
			seriesIDs = append(seriesIDs, registry.SeriesID(c.ID, d.id))
		}
	}

	tier, auto := db.PlanTier(query.after, query.before, query.points), true
	if tq := query.tier; tq != "" && tq != "auto" {
		n, err := strconv.Atoi(tq)
		if _, ok := db.TierEvery(n); err != nil || !ok {
			return 0, errors.New("unknown tier")
		}
		tier, auto = n, false
	}
	if auto {
		for ; tier > 0; tier-- {
			covered := true
			for _, sid := range seriesIDs {
				if !db.TierCovers(sid, tier, query.after) {
					covered = false
					break
				}
			}
			if covered {
				break
			}
		}
	}

	return tier, nil
}
