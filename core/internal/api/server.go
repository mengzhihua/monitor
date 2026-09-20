// Package api serves the REST/WebSocket API and the embedded dashboard.
package api

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

//go:embed all:ui/dist
var uiFS embed.FS

type Options struct {
	Version   string
	StartedAt time.Time
	AllowFrom []string
	Token     string
	Health    *health.Engine // nil = alarms API disabled
	Logger    *slog.Logger
}

type Server struct {
	reg   *registry.Registry
	db    *tsdb.Store
	sched *collect.Scheduler
	opt   Options
	log   *slog.Logger
	live  *liveHub
	nets  []*net.IPNet
	mux   *http.ServeMux
}

func New(reg *registry.Registry, db *tsdb.Store, sched *collect.Scheduler, opt Options) (*Server, error) {
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	s := &Server{reg: reg, db: db, sched: sched, opt: opt, log: opt.Logger, mux: http.NewServeMux()}
	for _, c := range opt.AllowFrom {
		if !strings.Contains(c, "/") {
			if strings.Contains(c, ":") {
				c += "/128"
			} else {
				c += "/32"
			}
		}
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, fmt.Errorf("web.allow_from %q: %w", c, err)
		}
		s.nets = append(s.nets, n)
	}
	s.live = newLiveHub(reg, s.log)
	s.routes()
	return s, nil
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /api/v1/info", s.handleInfo)
	m.HandleFunc("GET /api/v1/charts", s.handleCharts)
	m.HandleFunc("GET /api/v1/chart", s.handleChart)
	m.HandleFunc("GET /api/v1/data", s.handleData)
	m.HandleFunc("GET /api/v1/allmetrics", s.handleAllMetrics)
	m.HandleFunc("GET /api/v1/collectors", s.handleCollectors)
	m.HandleFunc("GET /api/v1/live", s.live.handle)
	m.HandleFunc("GET /api/v1/alarms", s.handleAlarms)
	m.HandleFunc("GET /api/v1/alarm_log", s.handleAlarmLog)
	m.HandleFunc("GET /api/v1/alarm_rules", s.handleAlarmRules)
	m.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		s.writePrometheus(w)
	})
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	m.Handle("/", s.uiHandler())
}

func (s *Server) Handler() http.Handler {
	return s.guard(s.mux)
}

// guard enforces web.allow_from and web.token for /api and /metrics.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.nets) > 0 {
			host, _, _ := net.SplitHostPort(r.RemoteAddr)
			ip := net.ParseIP(host)
			ok := false
			for _, n := range s.nets {
				if ip != nil && n.Contains(ip) {
					ok = true
					break
				}
			}
			if !ok {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		if s.opt.Token != "" && (strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/metrics") {
			if requestToken(r) != s.opt.Token {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requestToken extracts the caller's credential from, in order: the
// Authorization header, a `bearer.<token>` WebSocket subprotocol (browsers
// cannot set headers on WebSocket upgrades) or a `token` query parameter.
func requestToken(r *http.Request) string {
	if tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); tok != "" && tok != r.Header.Get("Authorization") {
		return tok
	}
	for _, p := range websocket.Subprotocols(r) {
		if strings.HasPrefix(p, wsTokenProto) {
			// base64url (no padding) keeps any token inside the subprotocol
			// token grammar (RFC 7230 tchar)
			if tok, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(p, wsTokenProto)); err == nil {
				return string(tok)
			}
			return ""
		}
	}
	return r.URL.Query().Get("token")
}

func (s *Server) uiHandler() http.Handler {
	sub, err := fs.Sub(uiFS, "ui/dist")
	if err != nil {
		panic(err)
	}
	fsrv := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(sub, "index.html"); err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, "<h1>monitord</h1><p>Dashboard not built. API: <a href=\"/api/v1/info\">/api/v1/info</a>, <a href=\"/api/v1/charts\">/api/v1/charts</a></p>")
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(sub, p); err != nil { // SPA fallback
			r.URL.Path = "/"
		}
		fsrv.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	h := s.reg.Host
	charts := s.reg.Charts()
	var dims int
	for _, c := range charts {
		dims += len(c.Dims())
	}
	writeJSON(w, map[string]any{
		"version":       s.opt.Version,
		"mode":          "agent",
		"host":          h,
		"uptime":        int64(time.Since(s.opt.StartedAt).Seconds()),
		"charts_count":  len(charts),
		"metrics_count": dims,
		"collectors":    s.sched.Status(),
		"alarms":        s.alarmSummary(),
		"db": map[string]any{
			"tiers": s.db.Tiers(), "dir": s.db.Dir(),
		},
	})
}

func (s *Server) handleCharts(w http.ResponseWriter, r *http.Request) {
	charts := s.reg.Charts()
	out := make(map[string]any, len(charts))
	for _, c := range charts {
		out[c.ID] = chartJSON(c, s.db)
	}
	writeJSON(w, map[string]any{"hostname": s.reg.Host.Hostname, "update_every": s.reg.Host.UpdateEvery, "charts_count": len(charts), "charts": out})
}

func chartJSON(c *registry.Chart, db *tsdb.Store) map[string]any {
	var first, last int64
	dims := c.Dims()
	for _, d := range dims {
		f, l, ok := db.Bounds(registry.SeriesID(c.ID, d.ID))
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
	return map[string]any{
		"id": c.ID, "context": c.Context, "family": c.Family, "title": c.Title, "units": c.Units,
		"chart_type": c.Type, "priority": c.Priority, "update_every": c.UpdateEvery, "plugin": c.Plugin,
		"module": c.Module, "labels": c.Labels, "dimensions": dims, "first_entry": first, "last_entry": last,
	}
}

func (s *Server) handleChart(w http.ResponseWriter, r *http.Request) {
	c, ok := s.reg.Chart(r.URL.Query().Get("chart"))
	if !ok {
		http.Error(w, "chart not found", http.StatusNotFound)
		return
	}
	writeJSON(w, chartJSON(c, s.db))
}

// parseTime accepts unix seconds or a relative offset (negative = seconds
// before now / before `before`).
func parseTime(s string, rel int64, def int64) int64 {
	if s == "" {
		return def
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	if n <= 0 {
		return rel + n
	}
	return n
}

// handleData implements /api/v1/data?chart=&after=&before=&points=&group=&dimensions=&options=
func (s *Server) handleData(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, ok := s.reg.Chart(q.Get("chart"))
	if !ok {
		http.Error(w, "chart not found", http.StatusNotFound)
		return
	}
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

	var dims []*registry.Dimension
	var ids, names []string
	for _, d := range c.Dims() {
		if d.Hidden && !showHidden && len(want) == 0 {
			continue
		}
		if len(want) > 0 && !want[d.ID] && !want[d.Name] {
			continue
		}
		dims = append(dims, d)
		ids = append(ids, d.ID)
		names = append(names, d.Name)
	}

	// tier: ""/"auto" lets the planner pick by range/points (falling back to
	// finer tiers while the planned one has nothing for this chart); a digit
	// forces that tier.
	tier, auto := s.db.PlanTier(after, before, points), true
	if tq := q.Get("tier"); tq != "" && tq != "auto" {
		n, err := strconv.Atoi(tq)
		if _, ok := s.db.TierEvery(n); err != nil || !ok {
			http.Error(w, "unknown tier", http.StatusBadRequest)
			return
		}
		tier, auto = n, false
	}
	var series [][]tsdb.Bucket
	for {
		series = series[:0]
		empty := true
		for _, d := range dims {
			bs, err := s.db.QueryTier(registry.SeriesID(c.ID, d.ID), tier, after, before)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if len(bs) > 0 {
				empty = false
			}
			series = append(series, bs)
		}
		if !auto || !empty || tier == 0 {
			break
		}
		tier--
	}
	every, _ := s.db.TierEvery(tier)
	res := tsdb.AggregateBuckets(series, every, after, before, points, group)

	// result.data rows: [time, dim1, dim2, ...]; NaN → null
	rows := make([][]any, 0, len(res.Times))
	var minV, maxV = math.Inf(1), math.Inf(-1)
	lastFilled := 0
	for i, t := range res.Times {
		row := make([]any, 0, len(series)+1)
		row = append(row, t)
		empty := true
		for _, sv := range res.Values {
			v := sv[i]
			if math.IsNaN(v) {
				row = append(row, nil)
				continue
			}
			empty = false
			row = append(row, round3(v))
			if v < minV {
				minV = v
			}
			if v > maxV {
				maxV = v
			}
		}
		rows = append(rows, row)
		if !empty {
			lastFilled = len(rows)
		}
	}
	// drop trailing empty buckets (the current, still-collecting second)
	if lastFilled < len(rows) && len(rows) > 1 {
		rows = rows[:max(lastFilled, 1)]
	}
	if math.IsInf(minV, 0) {
		minV, maxV = 0, 0
	}
	labels := append([]string{"time"}, names...)
	writeJSON(w, map[string]any{
		"api":               1,
		"id":                c.ID,
		"name":              c.ID,
		"context":           c.Context,
		"units":             c.Units,
		"chart_type":        c.Type,
		"update_every":      c.UpdateEvery,
		"view_update_every": res.Step,
		"tier":              tier,
		"after":             res.After,
		"before":            res.Before,
		"points":            len(rows),
		"group":             group,
		"dimension_ids":     ids,
		"dimension_names":   names,
		"min":               round3(minV),
		"max":               round3(maxV),
		"result":            map[string]any{"labels": labels, "data": rows},
	})
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

func (s *Server) handleAllMetrics(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Query().Get("format") {
	case "prometheus":
		s.writePrometheus(w)
	default:
		out := map[string]any{}
		for _, c := range s.reg.Charts() {
			ts, vals := c.LastValues()
			dims := map[string]any{}
			for _, d := range c.Dims() {
				if v, ok := vals[d.ID]; ok {
					dims[d.ID] = map[string]any{"name": d.Name, "value": round3(v)}
				}
			}
			out[c.ID] = map[string]any{"name": c.ID, "context": c.Context, "family": c.Family, "units": c.Units, "last_updated": ts, "dimensions": dims}
		}
		writeJSON(w, out)
	}
}

func promName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func (s *Server) writePrometheus(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	host := s.reg.Host.Hostname
	charts := s.reg.Charts()
	// HELP/TYPE may appear once per metric family, so charts sharing a
	// context (per-core cpu, per-disk io, ...) are grouped by metric name.
	sort.SliceStable(charts, func(i, j int) bool { return charts[i].Context < charts[j].Context })
	seen := map[string]bool{}
	for _, c := range charts {
		ts, vals := c.LastValues()
		if len(vals) == 0 {
			continue
		}
		metric := "monitor_" + promName(c.Context)
		if !seen[metric] {
			seen[metric] = true
			fmt.Fprintf(w, "# HELP %s %s (%s)\n# TYPE %s gauge\n", metric, c.Title, c.Units, metric)
		}
		keys := make([]string, 0, len(vals))
		for k := range vals {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			var lbl strings.Builder
			fmt.Fprintf(&lbl, `chart=%q,family=%q,dimension=%q,instance=%q`, c.ID, c.Family, k, host)
			for lk, lv := range c.Labels {
				fmt.Fprintf(&lbl, `,%s=%q`, promName(lk), lv)
			}
			fmt.Fprintf(w, "%s{%s} %g %d\n", metric, lbl.String(), vals[k], ts*1000)
		}
	}
}

func (s *Server) handleCollectors(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"available": collect.Available(), "status": s.sched.Status()})
}

func (s *Server) alarmSummary() any {
	if s.opt.Health == nil {
		return nil
	}
	return s.opt.Health.Summary()
}
