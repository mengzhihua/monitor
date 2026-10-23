package api

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

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
	Min             float64
	Max             float64
	Labels          []string
	Rows            [][]any
	Anomaly         []float64 // per-dimension current 0–100 bits
	GroupBy         string
}

func (d *dataResult) jsonMap() map[string]any {
	m := map[string]any{
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
		"dimension_anomaly": d.Anomaly,
		"anomaly":           anomalyBits(d.Anomaly),
		"min":               round3(d.Min),
		"max":               round3(d.Max),
		"result":            map[string]any{"labels": d.Labels, "data": d.Rows},
	}
	if d.GroupBy != "" {
		m["group_by"] = d.GroupBy
	}
	return m
}

func anomalyBits(rates []float64) []int {
	out := make([]int, len(rates))
	for i, v := range rates {
		if v >= 50 {
			out[i] = 1
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

// dataRows preserves interior gaps and trims only empty trailing buckets.
func dataRows(times []int64, values [][]float64) ([][]any, float64, float64) {
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

	return rows, minV, maxV
}
