package api

import (
	"math"
	"net/http"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func (s *Server) handleNodesV2(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.nodesPayload(r, 2))
}

func (s *Server) handleNodesV3(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.nodesPayload(r, 3))
}

func (s *Server) serveDataByNode(w http.ResponseWriter, r *http.Request, api int) {
	q := r.URL.Query()
	q.Del("group_by")
	views := s.dataViews(userOf(r))
	if len(views) == 0 {
		http.Error(w, "no nodes", http.StatusNotFound)
		return
	}
	type series struct {
		name   string
		times  []int64
		values []float64
		units  string
		ctype  registry.ChartType
		ctx    string
	}
	var parts []series
	for _, v := range views {
		d, _, errMsg := s.queryData(v, q, api)
		if errMsg != "" || d == nil || len(d.Rows) == 0 {
			continue
		}
		times := make([]int64, len(d.Rows))
		vals := make([]float64, len(d.Rows))
		for i, row := range d.Rows {
			if len(row) == 0 {
				continue
			}
			if t, ok := row[0].(int64); ok {
				times[i] = t
			} else if f, ok := row[0].(float64); ok {
				times[i] = int64(f)
			}
			sum, n := 0.0, 0
			for _, cell := range row[1:] {
				switch x := cell.(type) {
				case float64:
					sum += x
					n++
				case int:
					sum += float64(x)
					n++
				}
			}
			if n == 0 {
				vals[i] = math.NaN()
			} else {
				vals[i] = sum
			}
		}
		name := v.hostname
		if name == "" {
			name = v.id
			if name == "" {
				name = "local"
			}
		}
		parts = append(parts, series{name: name, times: times, values: vals, units: d.Units, ctype: d.ChartType, ctx: d.Context})
	}
	if len(parts) == 0 {
		http.Error(w, "no data", http.StatusNotFound)
		return
	}
	times := parts[0].times
	ids := make([]string, len(parts))
	names := make([]string, len(parts))
	rows := make([][]any, len(times))
	minV, maxV := math.Inf(1), math.Inf(-1)
	for i, t := range times {
		row := make([]any, 0, len(parts)+1)
		row = append(row, t)
		for j, p := range parts {
			if i >= len(p.values) || math.IsNaN(p.values[i]) {
				row = append(row, nil)
				continue
			}
			val := round3(p.values[i])
			row = append(row, val)
			if p.values[i] < minV {
				minV = p.values[i]
			}
			if p.values[i] > maxV {
				maxV = p.values[i]
			}
			_ = j
		}
		rows[i] = row
	}
	for i, p := range parts {
		ids[i], names[i] = p.name, p.name
	}
	if math.IsInf(minV, 0) {
		minV, maxV = 0, 0
	}
	ctx := q.Get("context")
	if ctx == "" {
		ctx = parts[0].ctx
	}
	out := &dataResult{
		API: api, ID: ctx, Name: ctx, Context: ctx, Units: parts[0].units, ChartType: parts[0].ctype,
		DimensionIDs: ids, DimensionNames: names, Min: minV, Max: maxV, GroupBy: "node",
		Labels: append([]string{"time"}, names...), Rows: rows,
	}
	writeData(w, q, out)
}

func (s *Server) dataViews(u User) []*view {
	views := []*view{}
	if v, ok := s.resolve(""); ok {
		views = append(views, v)
	}
	if s.opt.Nodes != nil {
		for _, n := range s.opt.Nodes.List() {
			if !s.roomAllowed(u, n.ID) {
				continue
			}
			views = append(views, &view{id: n.ID, hostname: n.Host.Hostname, reg: n.Registry(), db: n.DB(), node: n})
		}
	}
	return views
}

func viewLabel(v *view) string {
	if v.hostname != "" {
		return v.hostname
	}
	if v.id != "" {
		return v.id
	}
	return "local"
}

// serveDataByNodeDim is group_by=node,dimension: one column per node×dimension.
func (s *Server) serveDataByNodeDim(w http.ResponseWriter, r *http.Request, api int) {
	q := r.URL.Query()
	q.Del("group_by")
	views := s.dataViews(userOf(r))
	if len(views) == 0 {
		http.Error(w, "no nodes", http.StatusNotFound)
		return
	}
	type part struct {
		label  string
		result *dataResult
	}
	var parts []part
	for _, v := range views {
		d, _, errMsg := s.queryData(v, q, api)
		if errMsg != "" || d == nil || len(d.Rows) == 0 {
			continue
		}
		parts = append(parts, part{label: viewLabel(v), result: d})
	}
	if len(parts) == 0 {
		http.Error(w, "no data", http.StatusNotFound)
		return
	}
	nTimes := len(parts[0].result.Rows)
	var ids, names []string
	var anom []float64
	for _, p := range parts {
		for i, id := range p.result.DimensionIDs {
			ids = append(ids, p.label+"."+id)
			name := id
			if i < len(p.result.DimensionNames) && p.result.DimensionNames[i] != "" {
				name = p.result.DimensionNames[i]
			}
			names = append(names, p.label+"."+name)
			if i < len(p.result.Anomaly) {
				anom = append(anom, p.result.Anomaly[i])
			} else {
				anom = append(anom, 0)
			}
		}
	}
	rows := make([][]any, nTimes)
	minV, maxV := math.Inf(1), math.Inf(-1)
	for i := 0; i < nTimes; i++ {
		row := []any{parts[0].result.Rows[i][0]}
		for _, p := range parts {
			src := p.result.Rows
			if i >= len(src) {
				for range p.result.DimensionIDs {
					row = append(row, nil)
				}
				continue
			}
			for _, cell := range src[i][1:] {
				row = append(row, cell)
				if f, ok := cell.(float64); ok {
					if f < minV {
						minV = f
					}
					if f > maxV {
						maxV = f
					}
				}
			}
		}
		rows[i] = row
	}
	if math.IsInf(minV, 0) {
		minV, maxV = 0, 0
	}
	head := parts[0].result
	out := &dataResult{
		API: api, ID: head.ID, Name: head.Name, Context: head.Context, Units: head.Units, ChartType: head.ChartType,
		DimensionIDs: ids, DimensionNames: names, Min: minV, Max: maxV, Anomaly: anom, GroupBy: "node,dimension",
		Labels: append([]string{"time"}, names...), Rows: rows,
	}
	writeData(w, q, out)
}
