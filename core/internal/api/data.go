package api

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

// handleData implements /api/v1/data?chart=&context=&after=&before=&points=&group=&dimensions=&format=
func (s *Server) handleData(w http.ResponseWriter, r *http.Request) {
	s.serveData(w, r, 1)
}

func (s *Server) handleDataV2(w http.ResponseWriter, r *http.Request) {
	s.serveData(w, r, 2)
}

func (s *Server) handleContextsV2(w http.ResponseWriter, r *http.Request) {
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	writeJSON(w, s.contextsPayload(v, 2))
}

func (s *Server) handleNodesV2(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.nodesPayload(r, 2))
}

func (s *Server) serveData(w http.ResponseWriter, r *http.Request, api int) {
	q := r.URL.Query()
	if strings.EqualFold(q.Get("group_by"), "node") {
		s.serveDataByNode(w, r, api)
		return
	}
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	if v.node != nil && s.opt.Nodes != nil && s.opt.Nodes.Storage() == "proxy" && v.node.Online() {
		args := map[string]string{"chart": q.Get("chart"), "after": q.Get("after"), "before": q.Get("before"), "points": q.Get("points")}
		raw, err := v.node.Query(r.Context(), args)
		if err == nil && len(raw) > 0 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(raw)
			return
		}
	}
	out, code, errMsg := s.queryData(v, q, api)
	if errMsg != "" {
		http.Error(w, errMsg, code)
		return
	}
	writeData(w, q, out)
}

func (s *Server) serveDataByNode(w http.ResponseWriter, r *http.Request, api int) {
	q := r.URL.Query()
	q.Del("group_by")
	views := []*view{}
	if v, ok := s.resolve(""); ok {
		views = append(views, v)
	}
	if s.opt.Nodes != nil {
		for _, n := range s.opt.Nodes.List() {
			views = append(views, &view{id: n.ID, hostname: n.Host.Hostname, reg: n.Registry(), db: n.DB(), node: n})
		}
	}
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
		DimensionIDs: ids, DimensionNames: names, Min: minV, Max: maxV,
		Labels: append([]string{"time"}, names...), Rows: rows,
	}
	writeData(w, q, out)
}

type dataResult struct {
	API             int
	Node            string
	ID              string
	Name            string
	Context         string
	Units           string
	ChartType       registry.ChartType
	UpdateEvery     int
	ViewUpdateEvery int64
	Tier            int
	After           int64
	Before          int64
	Group           tsdb.GroupFunc
	DimensionIDs    []string
	DimensionNames  []string
	Anomaly         []int
	Min             float64
	Max             float64
	Labels          []string
	Rows            [][]any
}

func (d *dataResult) jsonMap() map[string]any {
	return map[string]any{
		"api":               d.API,
		"node":              d.Node,
		"id":                d.ID,
		"name":              d.Name,
		"context":           d.Context,
		"units":             d.Units,
		"chart_type":        d.ChartType,
		"update_every":      d.UpdateEvery,
		"view_update_every": d.ViewUpdateEvery,
		"tier":              d.Tier,
		"after":             d.After,
		"before":            d.Before,
		"points":            len(d.Rows),
		"group":             d.Group,
		"dimension_ids":     d.DimensionIDs,
		"dimension_names":   d.DimensionNames,
		"anomaly":           d.Anomaly,
		"min":               round3(d.Min),
		"max":               round3(d.Max),
		"result":            map[string]any{"labels": d.Labels, "data": d.Rows},
	}
}

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

func (s *Server) queryData(v *view, q url.Values, api int) (*dataResult, int, string) {
	charts, errMsg := chartsForData(v.reg, q.Get("chart"), q.Get("context"))
	if errMsg != "" {
		code := http.StatusNotFound
		if errMsg == "chart or context required" {
			code = http.StatusBadRequest
		}
		return nil, code, errMsg
	}
	db := v.db
	now := time.Now().Unix()
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
	showHidden := strings.Contains(opts, "all-dimensions")

	type dimRef struct {
		id, name string
		charts   []*registry.Chart
	}
	seen := map[string]int{}
	var dims []dimRef
	for _, c := range charts {
		for _, d := range c.Dims() {
			if d.Hidden && !showHidden && len(want) == 0 {
				continue
			}
			if len(want) > 0 && !want[d.ID] && !want[d.Name] {
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
			dims = append(dims, dimRef{id: d.ID, name: name, charts: []*registry.Chart{c}})
		}
	}

	var seriesIDs []string
	for _, d := range dims {
		for _, c := range d.charts {
			seriesIDs = append(seriesIDs, registry.SeriesID(c.ID, d.id))
		}
	}

	tier, auto := db.PlanTier(after, before, points), true
	if tq := q.Get("tier"); tq != "" && tq != "auto" {
		n, err := strconv.Atoi(tq)
		if _, ok := db.TierEvery(n); err != nil || !ok {
			return nil, http.StatusBadRequest, "unknown tier"
		}
		tier, auto = n, false
	}
	if auto {
		for ; tier > 0; tier-- {
			covered := true
			for _, sid := range seriesIDs {
				if !db.TierCovers(sid, tier, after) {
					covered = false
					break
				}
			}
			if covered {
				break
			}
		}
	}

	every, _ := db.TierEvery(tier)
	ids := make([]string, 0, len(dims))
	names := make([]string, 0, len(dims))
	values := make([][]float64, 0, len(dims))
	var times []int64
	var step, resAfter, resBefore int64
	for _, d := range dims {
		parts := make([][]tsdb.Bucket, 0, len(d.charts))
		for _, c := range d.charts {
			bs, err := db.QueryTier(registry.SeriesID(c.ID, d.id), tier, after, before)
			if err != nil {
				return nil, http.StatusInternalServerError, err.Error()
			}
			parts = append(parts, bs)
		}
		agg := tsdb.AggregateBuckets(parts, every, after, before, points, group)
		if times == nil {
			times = agg.Times
			step, resAfter, resBefore = agg.Step, agg.After, agg.Before
		}
		values = append(values, sumAligned(agg.Values))
		ids = append(ids, d.id)
		names = append(names, d.name)
	}

	rows := make([][]any, 0, len(times))
	minV, maxV := math.Inf(1), math.Inf(-1)
	lastFilled := 0
	for i, t := range times {
		row := make([]any, 0, len(values)+1)
		row = append(row, t)
		empty := true
		for _, sv := range values {
			if i >= len(sv) || math.IsNaN(sv[i]) {
				row = append(row, nil)
				continue
			}
			empty = false
			val := round3(sv[i])
			row = append(row, val)
			if sv[i] < minV {
				minV = sv[i]
			}
			if sv[i] > maxV {
				maxV = sv[i]
			}
		}
		rows = append(rows, row)
		if !empty {
			lastFilled = len(rows)
		}
	}
	if lastFilled < len(rows) && len(rows) > 1 {
		rows = rows[:max(lastFilled, 1)]
	}
	if math.IsInf(minV, 0) {
		minV, maxV = 0, 0
	}

	head := charts[0]
	id, ctx := head.ID, head.Context
	everySec := head.UpdateEvery
	if len(charts) > 1 {
		id = q.Get("context")
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
	anomMap := s.anomalies()
	anomBits := make([]int, len(dims))
	for i, d := range dims {
		for _, c := range d.charts {
			if anomMap[registry.SeriesID(c.ID, d.id)] {
				anomBits[i] = 1
				break
			}
		}
	}
	return &dataResult{
		API: api, Node: v.id, ID: id, Name: id, Context: ctx, Units: head.Units,
		ChartType: head.Type, UpdateEvery: everySec, ViewUpdateEvery: step, Tier: tier,
		After: resAfter, Before: resBefore, Group: group, DimensionIDs: ids, DimensionNames: names,
		Anomaly: anomBits, Min: minV, Max: maxV, Labels: append([]string{"time"}, names...), Rows: rows,
	}, 0, ""
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

func writeData(w http.ResponseWriter, q url.Values, d *dataResult) {
	format := strings.ToLower(q.Get("format"))
	switch format {
	case "csv", "ssv":
		sep := ","
		if format == "ssv" {
			sep = " "
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		} else {
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		}
		fmt.Fprintln(w, strings.Join(d.Labels, sep))
		for _, row := range d.Rows {
			cells := make([]string, len(row))
			for i, cell := range row {
				switch v := cell.(type) {
				case nil:
				case float64:
					cells[i] = strconv.FormatFloat(v, 'g', -1, 64)
				case int64:
					cells[i] = strconv.FormatInt(v, 10)
				default:
					cells[i] = fmt.Sprint(v)
				}
			}
			fmt.Fprintln(w, strings.Join(cells, sep))
		}
	case "jsonp":
		body, err := json.Marshal(d.jsonMap())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		cb := jsonpCallback(q.Get("callback"))
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		fmt.Fprintf(w, "%s(%s);", cb, body)
	default:
		writeJSON(w, d.jsonMap())
	}
}

func jsonpCallback(s string) string {
	if s == "" {
		return "callback"
	}
	if len(s) > 64 {
		return "callback"
	}
	for i, r := range s {
		if i == 0 && (r == '_' || unicode.IsLetter(r)) {
			continue
		}
		if r == '_' || r == '.' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		return "callback"
	}
	return s
}

func (s *Server) handleAlarmCount(w http.ResponseWriter, r *http.Request) {
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	var sum health.Summary
	if v.node != nil {
		for _, e := range v.node.Alarms() {
			switch e.Status {
			case health.StatusWarning:
				sum.Warning++
			case health.StatusCritical:
				sum.Critical++
			case health.StatusClear:
				sum.Normal++
			default:
				sum.Silent++
			}
		}
	} else if s.opt.Health != nil {
		sum = s.opt.Health.Summary()
	} else {
		http.Error(w, "health engine disabled", http.StatusNotFound)
		return
	}
	count := sum.Warning + sum.Critical
	if st := r.URL.Query().Get("status"); st != "" {
		count = 0
		for _, p := range strings.Split(st, ",") {
			switch strings.ToUpper(strings.TrimSpace(p)) {
			case "WARNING":
				count += sum.Warning
			case "CRITICAL":
				count += sum.Critical
			case "CLEAR", "NORMAL":
				count += sum.Normal
			case "UNDEFINED", "UNINITIALIZED", "REMOVED", "SILENT":
				count += sum.Silent
			}
		}
	}
	writeJSON(w, map[string]any{
		"status": true, "count": count,
		"normal": sum.Normal, "warning": sum.Warning, "critical": sum.Critical, "silent": sum.Silent,
	})
}

func (s *Server) handleBadge(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	label := q.Get("label")
	units := q.Get("units")
	color := "#64748b"
	var value float64
	have := false

	if alarm := q.Get("alarm"); alarm != "" {
		var alarms []health.Alarm
		if v.node != nil {
			for _, e := range v.node.Alarms() {
				alarms = append(alarms, alarmFromEntry(e))
			}
		} else if s.opt.Health != nil {
			alarms = s.opt.Health.Alarms()
		}
		for _, a := range alarms {
			if a.Name != alarm && a.Chart+"."+a.Name != alarm {
				continue
			}
			if !math.IsNaN(a.Value) {
				value, have = a.Value, true
			}
			if units == "" {
				units = a.Units
			}
			if label == "" {
				label = a.Name
			}
			switch a.Status {
			case health.StatusCritical:
				color = "#b91c1c"
			case health.StatusWarning:
				color = "#ca8a04"
			case health.StatusClear:
				color = "#15803d"
			}
			break
		}
	}
	if !have {
		charts, errMsg := chartsForData(v.reg, q.Get("chart"), q.Get("context"))
		if errMsg != "" {
			http.Error(w, errMsg, http.StatusNotFound)
			return
		}
		want := q.Get("dimension")
		if want == "" {
			want = q.Get("dimensions")
			if i := strings.Index(want, ","); i >= 0 {
				want = want[:i]
			}
		}
		sum, n := 0.0, 0
		for _, c := range charts {
			if label == "" {
				label = c.ID
			}
			if units == "" {
				units = c.Units
			}
			_, last := c.LastValues()
			if want != "" {
				if val, ok := last[want]; ok {
					sum += val
					n++
				}
				continue
			}
			for _, d := range c.Dims() {
				if d.Hidden {
					continue
				}
				if val, ok := last[d.ID]; ok {
					sum += val
					n++
					break
				}
			}
		}
		if n > 0 {
			value, have = sum, true
			if n > 1 {
				color = "#2563eb"
			} else {
				color = "#15803d"
			}
		}
	}
	if vc := q.Get("value_color"); vc != "" {
		if c := sanitizeColor(vc); c != "" {
			color = c
		}
	}
	prec := 2
	if p, err := strconv.Atoi(q.Get("precision")); err == nil && p >= 0 && p <= 6 {
		prec = p
	}
	valueText := "-"
	if have {
		valueText = strconv.FormatFloat(roundN(value, prec), 'f', prec, 64)
		if units != "" {
			valueText += " " + units
		}
	}
	if label == "" {
		label = "monitor"
	}
	writeBadgeSVG(w, label, valueText, color)
}

func roundN(v float64, prec int) float64 {
	p := math.Pow(10, float64(prec))
	return math.Round(v*p) / p
}

func sanitizeColor(s string) string {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "red":
		return "#b91c1c"
	case "green":
		return "#15803d"
	case "orange", "yellow":
		return "#ca8a04"
	case "blue":
		return "#2563eb"
	case "gray", "grey":
		return "#64748b"
	}
	if strings.HasPrefix(s, "#") && (len(s) == 4 || len(s) == 7) {
		for _, r := range s[1:] {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return ""
			}
		}
		return s
	}
	return ""
}

func writeBadgeSVG(w http.ResponseWriter, label, value, color string) {
	lw := 7*len(label) + 10
	vw := 7*len(value) + 10
	if lw < 40 {
		lw = 40
	}
	if vw < 30 {
		vw = 30
	}
	width := lw + vw
	escL, escV := html.EscapeString(label), html.EscapeString(value)
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" role="img" aria-label="%s: %s">`, width, escL, escV)
	fmt.Fprintf(w, `<linearGradient id="s" x2="0" y2="100%%"><stop offset="0" stop-color="#fff" stop-opacity=".1"/><stop offset="1" stop-opacity=".1"/></linearGradient>`)
	fmt.Fprintf(w, `<rect rx="3" width="%d" height="20" fill="#555"/>`, width)
	fmt.Fprintf(w, `<rect rx="3" x="%d" width="%d" height="20" fill="%s"/>`, lw, vw, html.EscapeString(color))
	fmt.Fprintf(w, `<rect x="%d" width="4" height="20" fill="%s"/>`, lw, html.EscapeString(color))
	fmt.Fprintf(w, `<rect rx="3" width="%d" height="20" fill="url(#s)"/>`, width)
	fmt.Fprintf(w, `<g fill="#fff" text-anchor="middle" font-family="DejaVu Sans,Verdana,Geneva,sans-serif" font-size="11">`)
	fmt.Fprintf(w, `<text x="%d" y="15" fill="#010101" fill-opacity=".3">%s</text><text x="%d" y="14">%s</text>`, lw/2, escL, lw/2, escL)
	fmt.Fprintf(w, `<text x="%d" y="15" fill="#010101" fill-opacity=".3">%s</text><text x="%d" y="14">%s</text>`, lw+vw/2, escV, lw+vw/2, escV)
	fmt.Fprintf(w, `</g></svg>`)
}
