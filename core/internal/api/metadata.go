package api

import (
	"net/http"
	"sort"

	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func (s *Server) handleCharts(w http.ResponseWriter, r *http.Request) {
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	charts := v.reg.Charts()
	out := make(map[string]chartMetadata, len(charts))
	// A snapshot scans all tracked dimensions. Share it across this response
	// instead of repeating the scan for every chart.
	anom, src := s.anomalies(), s.anomaly()
	for _, c := range charts {
		out[c.ID] = chartJSON(c, v.db, anom, src)
	}
	writeJSON(w, map[string]any{"node": v.id, "hostname": v.hostname, "update_every": v.reg.Host.UpdateEvery, "charts_count": len(charts), "charts": out})
}

// Explicit wire types avoid per-field maps and interface boxing for every
// dimension on each metadata poll. Dynamic bounds and anomaly state are still
// read for each response; no chart definition or response is cached.
type dimensionMetadata struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	Algorithm  registry.Algorithm `json:"algorithm"`
	Multiplier int64              `json:"multiplier"`
	Divisor    int64              `json:"divisor"`
	Hidden     bool               `json:"hidden"`
	Anomaly    bool               `json:"anomaly,omitempty"`
	Rate       *float64           `json:"dimension_anomaly,omitempty"`
}

type chartMetadata struct {
	ID          string              `json:"id"`
	Context     string              `json:"context"`
	Family      string              `json:"family"`
	Title       string              `json:"title"`
	Units       string              `json:"units"`
	Type        registry.ChartType  `json:"chart_type"`
	Priority    int                 `json:"priority"`
	UpdateEvery int                 `json:"update_every"`
	Plugin      string              `json:"plugin"`
	Module      string              `json:"module"`
	Labels      map[string]string   `json:"labels"`
	Dimensions  []dimensionMetadata `json:"dimensions"`
	FirstEntry  int64               `json:"first_entry"`
	LastEntry   int64               `json:"last_entry"`
	Anomaly     bool                `json:"anomaly"`
}

func chartJSON(c *registry.Chart, db tsdb.Reader, anom map[string]bool, src health.AnomalySource) chartMetadata {
	dims := c.Dims()
	first, last := chartBounds(c.ID, dims, db)
	dimOut := make([]dimensionMetadata, 0, len(dims))
	anomalous := false
	for _, d := range dims {
		m := dimensionMetadata{ID: d.ID, Name: d.Name, Algorithm: d.Algorithm, Multiplier: d.Multiplier, Divisor: d.Divisor, Hidden: d.Hidden}
		if anom[registry.SeriesID(c.ID, d.ID)] {
			m.Anomaly = true
			anomalous = true
		}
		if src != nil {
			if r, ok := src.Rate(c.ID, d.ID); ok {
				m.Rate = &r
				if r >= 50 {
					m.Anomaly = true
					anomalous = true
				}
			}
		}
		dimOut = append(dimOut, m)
	}
	return chartMetadata{
		ID: c.ID, Context: c.Context, Family: c.Family, Title: c.Title, Units: c.Units,
		Type: c.Type, Priority: c.Priority, UpdateEvery: c.UpdateEvery, Plugin: c.Plugin,
		Module: c.Module, Labels: c.Labels, Dimensions: dimOut, FirstEntry: first, LastEntry: last,
		Anomaly: anomalous,
	}
}

func chartBounds(chart string, dims []*registry.Dimension, db tsdb.Reader) (first, last int64) {
	for _, d := range dims {
		f, l, ok := db.Bounds(registry.SeriesID(chart, d.ID))
		if !ok {
			continue
		}
		if first == 0 || f < first {
			first = f
		}
		if l > last {
			last = l
		}
	}
	return first, last
}

func (s *Server) handleChart(w http.ResponseWriter, r *http.Request) {
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	c, ok := v.reg.Chart(r.URL.Query().Get("chart"))
	if !ok {
		http.Error(w, "chart not found", http.StatusNotFound)
		return
	}
	writeJSON(w, chartJSON(c, v.db, s.anomalies(), s.anomaly()))
}

func (s *Server) handleContexts(w http.ResponseWriter, r *http.Request) {
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	writeJSON(w, s.contextsPayload(v, 1))
}

type ctxInfo struct {
	Family     string   `json:"family"`
	Title      string   `json:"title"`
	Units      string   `json:"units"`
	ChartType  string   `json:"chart_type"`
	Priority   int      `json:"priority"`
	Plugin     string   `json:"plugin"`
	Charts     []string `json:"charts"`
	Dimensions []string `json:"dimensions"`
	FirstEntry int64    `json:"first_entry"`
	LastEntry  int64    `json:"last_entry"`
}

func (s *Server) contextsPayload(v *view, api int) map[string]any {
	out := map[string]*ctxInfo{}
	seenDimensions := map[string]map[string]bool{}
	for _, c := range v.reg.Charts() {
		ctx := c.Context
		if ctx == "" {
			ctx = c.ID
		}
		info, ok := out[ctx]
		if !ok {
			info = &ctxInfo{Family: c.Family, Title: c.Title, Units: c.Units, ChartType: string(c.Type),
				Priority: c.Priority, Plugin: c.Plugin}
			out[ctx] = info
			seenDimensions[ctx] = map[string]bool{}
		}
		info.Charts = append(info.Charts, c.ID)
		if c.Priority < info.Priority {
			info.Priority = c.Priority
		}
		seen := seenDimensions[ctx]
		dims := c.Dims()
		for _, d := range dims {
			if !seen[d.ID] {
				info.Dimensions = append(info.Dimensions, d.ID)
				seen[d.ID] = true
			}
		}
		// Contexts only expose series bounds; avoid building per-dimension JSON
		// and taking ML snapshots that would immediately be discarded.
		fe, le := chartBounds(c.ID, dims, v.db)
		if fe > 0 && (info.FirstEntry == 0 || fe < info.FirstEntry) {
			info.FirstEntry = fe
		}
		if le > info.LastEntry {
			info.LastEntry = le
		}
	}
	for _, info := range out {
		sort.Strings(info.Charts)
	}
	return map[string]any{"api": api, "node": v.id, "hostname": v.hostname, "contexts_count": len(out), "contexts": out}
}

func (s *Server) handleContextsV2(w http.ResponseWriter, r *http.Request) {
	s.serveContexts(w, r, 2)
}

func (s *Server) handleContextsV3(w http.ResponseWriter, r *http.Request) {
	s.serveContexts(w, r, 3)
}

func (s *Server) serveContexts(w http.ResponseWriter, r *http.Request, api int) {
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	writeJSON(w, s.contextsPayload(v, api))
}

func (s *Server) handleContext(w http.ResponseWriter, r *http.Request) {
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	id := r.URL.Query().Get("context")
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	if id == "" {
		http.Error(w, "context required", http.StatusBadRequest)
		return
	}
	payload := s.contextsPayload(v, 3)
	contexts, _ := payload["contexts"].(map[string]*ctxInfo)
	info, ok := contexts[id]
	if !ok {
		http.Error(w, "context not found", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"api": 3, "id": id, "context": info})
}
