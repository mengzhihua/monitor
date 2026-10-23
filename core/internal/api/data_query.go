package api

import (
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func chartsForData(reg *registry.Registry, chartID, ctx string) ([]*registry.Chart, string) {
	if chartID != "" {
		c, ok := reg.Chart(chartID)
		if !ok {
			return nil, "chart not found"
		}
		return []*registry.Chart{c}, ""
	}
	if ctx == "" {
		return nil, "chart or context required"
	}
	var out []*registry.Chart
	for _, c := range reg.Charts() {
		cc := c.Context
		if cc == "" {
			cc = c.ID
		}
		if cc == ctx || c.ID == ctx {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, "context not found"
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, ""
}

// queryData coordinates selection, storage reads, anomaly overlays and result
// shaping. HTTP routing and output formats live outside this query pipeline.
func (s *Server) queryData(v *view, q url.Values, api int) (*dataResult, int, string) {
	query := parseDataQuery(q, time.Now().Unix())
	charts, errMsg := chartsForData(v.reg, query.chart, query.context)
	if errMsg != "" {
		code := http.StatusNotFound
		if errMsg == "chart or context required" {
			code = http.StatusBadRequest
		}
		return nil, code, errMsg
	}
	dims := selectDataDimensions(charts, query)
	tier, err := planDataTier(v.db, query, dims)
	if err != nil {
		return nil, http.StatusBadRequest, err.Error()
	}
	series, err := readDataSeries(v.db, dims, query, tier)
	if err != nil {
		return nil, http.StatusInternalServerError, err.Error()
	}
	anom := s.applyDataAnomalies(dims, &series, query.anomalyBit)
	units := headUnits(charts)
	if query.anomalyBit {
		units = "%"
	}
	rows, minV, maxV := dataRows(series.times, series.values)
	ids := make([]string, 0, len(dims))
	names := make([]string, 0, len(dims))
	for _, d := range dims {
		ids = append(ids, d.id)
		names = append(names, d.name)
	}
	head := charts[0]
	id, ctx := head.ID, head.Context
	everySec := head.UpdateEvery
	if len(charts) > 1 {
		id = query.context
		if id == "" {
			id = head.Context
		}
		ctx = id
		for _, c := range charts[1:] {
			if c.UpdateEvery > 0 && (everySec == 0 || c.UpdateEvery < everySec) {
				everySec = c.UpdateEvery
			}
		}
	}
	gb := query.groupBy
	if gb == "" && len(charts) > 1 {
		gb = "dimension"
	}
	return &dataResult{
		API: api, Node: v.id, ID: id, Name: id, Context: ctx, Units: units,
		ChartType: head.Type, UpdateEvery: everySec, ViewUpdateEvery: series.step, Tier: tier,
		After: series.after, Before: series.before, Group: query.group, DimensionIDs: ids, DimensionNames: names,
		Min: minV, Max: maxV, Labels: append([]string{"time"}, names...), Rows: rows,
		Anomaly: anom, GroupBy: gb,
	}, 0, ""
}

func headUnits(charts []*registry.Chart) string {
	if len(charts) == 0 {
		return ""
	}
	return charts[0].Units
}
