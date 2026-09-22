// Package api serves the REST/WebSocket API and the embedded dashboard.
package api

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/mengzhihua/monitor/core/internal/hub"
	"github.com/mengzhihua/monitor/core/internal/ingest"
	"github.com/mengzhihua/monitor/core/internal/plugins"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/stream"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

//go:embed all:ui/dist
var uiFS embed.FS

type Options struct {
	Version   string
	Mode      string // agent | hub (informational)
	StartedAt time.Time
	AllowFrom []string
	// Token is the legacy single admin credential; Users adds named
	// credentials with roles. With neither set the API is anonymous (admin).
	Token  string
	Users  []User
	Health *health.Engine // nil = alarms API disabled
	Logger *slog.Logger
	// Plugins exposes external plugins.d processes in /api/v1/collectors (optional).
	Plugins *plugins.Manager
	// Nodes enables hub mode: /api/v1/stream ingestion and node= routing.
	Nodes *hub.Nodes
	// Stream reports this agent's own upstream connection in /api/v1/info.
	Stream *stream.Client
	// ExtraFunctions are hub-side functions (e.g. streaming) merged with collectors.
	ExtraFunctions []collect.Function
	// Cluster fans node lookups out to peer hubs.
	Cluster *hub.Cluster
	// Org is the Space/Room/claim store (hub mode).
	Org *hub.Org
	// PeerToken authenticates POST /api/v1/hub/ring from sibling hubs.
	PeerToken string
	// OIDC enables browser login against an identity provider.
	OIDC *OIDCConfig
}

type Server struct {
	reg    *registry.Registry
	db     *tsdb.Store
	sched  *collect.Scheduler
	opt    Options
	log    *slog.Logger
	live   *liveHub
	nets   []*net.IPNet
	mux    *http.ServeMux
	ingest *ingest.Mapper
	otlp   *ingest.Mapper
	oidc   *oidcState
}

func New(reg *registry.Registry, db *tsdb.Store, sched *collect.Scheduler, opt Options) (*Server, error) {
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	s := &Server{reg: reg, db: db, sched: sched, opt: opt, log: opt.Logger, mux: http.NewServeMux(),
		ingest: ingest.NewMapper(ingest.Options{Prefix: "om", Plugin: "ingest", Module: "openmetrics", Family: "openmetrics"}),
		otlp:   ingest.NewMapper(ingest.Options{Prefix: "otlp", Plugin: "ingest", Module: "otlp", Family: "otlp"})}
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
	if err := validateUsers(opt.Users); err != nil {
		return nil, err
	}
	s.live = newLiveHub(reg, s.log)
	s.oidc = newOIDC(opt.OIDC)
	s.routes()
	return s, nil
}

// PublishNodeSample / PublishNodeAlarm feed a remote node's activity into
// the live WebSocket; wire them into hub.Options.
func (s *Server) PublishNodeSample(nodeID, chartID string, ts int64, values map[string]float64) {
	s.live.broadcastNode(nodeID, chartID, ts, values)
}

func (s *Server) PublishNodeAlarm(nodeID string, e health.LogEntry) {
	s.publishAlarm(nodeID, e)
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /api/v1/info", s.handleInfo)
	m.HandleFunc("GET /api/v1/charts", s.handleCharts)
	m.HandleFunc("GET /api/v1/chart", s.handleChart)
	m.HandleFunc("GET /api/v1/data", s.handleData)
	m.HandleFunc("GET /api/v1/allmetrics", s.handleAllMetrics)
	m.HandleFunc("GET /api/v1/alarm_count", s.handleAlarmCount)
	m.HandleFunc("GET /api/v1/badge.svg", s.handleBadge)
	m.HandleFunc("GET /api/v2/data", s.handleDataV2)
	m.HandleFunc("GET /api/v2/contexts", s.handleContextsV2)
	m.HandleFunc("GET /api/v2/nodes", s.handleNodesV2)
	m.HandleFunc("GET /api/v2/badge.svg", s.handleBadge)
	m.HandleFunc("GET /api/v1/collectors", s.handleCollectors)
	m.HandleFunc("GET /api/v1/functions", s.handleFunctions)
	m.HandleFunc("GET /api/v1/function", s.handleFunction)
	m.HandleFunc("GET /api/v1/live", s.handleLive)
	m.HandleFunc("GET /api/v1/alarms", s.handleAlarms)
	m.HandleFunc("GET /api/v1/alarm_log", s.handleAlarmLog)
	m.HandleFunc("GET /api/v1/alarm_rules", s.handleAlarmRules)
	m.HandleFunc("GET /api/v1/alarm_variables", s.handleAlarmVariables)
	m.HandleFunc("GET /api/v1/alarms/silence", s.handleSilence)
	m.HandleFunc("POST /api/v1/alarms/silence", s.handleSilence)
	m.HandleFunc("GET /api/v1/contexts", s.handleContexts)
	m.HandleFunc("GET /api/v1/weights", s.handleWeights)
	m.HandleFunc("GET /api/v1/logs", s.handleLogs)
	m.HandleFunc("POST /api/v1/ingest/openmetrics", s.handleIngestOpenMetrics)
	m.HandleFunc("PUT /api/v1/ingest/openmetrics", s.handleIngestOpenMetrics)
	m.HandleFunc("POST /api/v1/ingest/otlp", s.handleIngestOTLP)
	m.HandleFunc("POST /v1/metrics", s.handleIngestOTLP)
	m.HandleFunc("GET /api/v1/nodes", s.handleNodes)
	m.HandleFunc("DELETE /api/v1/nodes", s.handleForgetNode)
	m.HandleFunc("GET /api/v1/hub/spaces", s.handleSpaces)
	m.HandleFunc("POST /api/v1/hub/spaces", s.handleSpaces)
	m.HandleFunc("DELETE /api/v1/hub/spaces", s.handleSpaces)
	m.HandleFunc("GET /api/v1/hub/rooms", s.handleRooms)
	m.HandleFunc("POST /api/v1/hub/rooms", s.handleRooms)
	m.HandleFunc("DELETE /api/v1/hub/rooms", s.handleRooms)
	m.HandleFunc("PUT /api/v1/hub/rooms", s.handleRooms)
	m.HandleFunc("GET /api/v1/hub/claim-tokens", s.handleClaimTokens)
	m.HandleFunc("POST /api/v1/hub/claim-tokens", s.handleClaimTokens)
	m.HandleFunc("POST /api/v1/claim", s.handleClaimRedeem)
	m.HandleFunc("GET /api/v1/hub/config", s.handleHubConfig)
	m.HandleFunc("PUT /api/v1/hub/config", s.handleHubConfig)
	m.HandleFunc("GET /api/v1/agent/config", s.handleAgentConfig)
	m.HandleFunc("POST /api/v1/hub/ring", s.handleRing)
	m.HandleFunc("GET /api/v1/auth/oidc/login", s.handleOIDCLogin)
	m.HandleFunc("GET /api/v1/auth/oidc/callback", s.handleOIDCCallback)
	if s.opt.Nodes != nil {
		m.HandleFunc("GET "+stream.Path, s.opt.Nodes.HandleStream)
	}
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
		if publicAPI(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/metrics" || strings.HasPrefix(r.URL.Path, "/v1/") {
			u, ok := s.authenticate(r)
			if !ok {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if !u.Role.allows(r) {
				http.Error(w, "forbidden: role "+string(u.Role)+" may not "+r.Method+" "+r.URL.Path, http.StatusForbidden)
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), userKey{}, u))
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
	mode := s.opt.Mode
	if mode == "" {
		mode = "agent"
	}
	out := map[string]any{
		"version":       s.opt.Version,
		"mode":          mode,
		"host":          h,
		"uptime":        int64(time.Since(s.opt.StartedAt).Seconds()),
		"charts_count":  len(charts),
		"metrics_count": dims,
		"collectors":    s.sched.Status(),
		"plugins":       s.pluginStatus(),
		"alarms":        s.alarmSummary(),
		"db": map[string]any{
			"tiers": s.db.Tiers(), "dir": s.db.Dir(),
		},
		"user": userOf(r),
	}
	if s.opt.Nodes != nil {
		out["nodes_count"] = len(s.opt.Nodes.List()) + 1
		out["streaming_enabled"] = s.opt.Nodes.IngestEnabled()
	}
	if s.opt.Stream != nil {
		out["stream"] = s.opt.Stream.Status()
	}
	writeJSON(w, out)
}

func (s *Server) handleCharts(w http.ResponseWriter, r *http.Request) {
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	charts := v.reg.Charts()
	out := make(map[string]any, len(charts))
	for _, c := range charts {
		out[c.ID] = chartJSON(c, v.db)
	}
	writeJSON(w, map[string]any{"node": v.id, "hostname": v.hostname, "update_every": v.reg.Host.UpdateEvery, "charts_count": len(charts), "charts": out})
}

func chartJSON(c *registry.Chart, db tsdb.Reader) map[string]any {
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
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	c, ok := v.reg.Chart(r.URL.Query().Get("chart"))
	if !ok {
		http.Error(w, "chart not found", http.StatusNotFound)
		return
	}
	writeJSON(w, chartJSON(c, v.db))
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

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

func (s *Server) handleAllMetrics(w http.ResponseWriter, r *http.Request) {
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	switch r.URL.Query().Get("format") {
	case "prometheus":
		s.writePrometheusFor(w, v)
	case "csv":
		s.writeAllMetricsCSV(w, v)
	case "shell":
		s.writeAllMetricsShell(w, v)
	default:
		out := map[string]any{}
		for _, c := range v.reg.Charts() {
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

func (s *Server) writeAllMetricsCSV(w http.ResponseWriter, v *view) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	fmt.Fprintln(w, "chart,dimension,value,timestamp")
	for _, c := range v.reg.Charts() {
		ts, vals := c.LastValues()
		keys := make([]string, 0, len(vals))
		for k := range vals {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "%s,%s,%g,%d\n", c.ID, k, vals[k], ts)
		}
	}
}

func (s *Server) writeAllMetricsShell(w http.ResponseWriter, v *view) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "# hostname=%s\n", v.hostname)
	for _, c := range v.reg.Charts() {
		_, vals := c.LastValues()
		keys := make([]string, 0, len(vals))
		for k := range vals {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		prefix := "NETDATA_" + promName(c.ID)
		for _, k := range keys {
			fmt.Fprintf(w, "%s_%s=%g\n", prefix, promName(k), vals[k])
		}
	}
}

func (s *Server) handleContexts(w http.ResponseWriter, r *http.Request) {
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	writeJSON(w, s.contextsPayload(v, 1))
}

func (s *Server) contextsPayload(v *view, api int) map[string]any {
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
	out := map[string]*ctxInfo{}
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
		}
		info.Charts = append(info.Charts, c.ID)
		if c.Priority < info.Priority {
			info.Priority = c.Priority
		}
		seen := map[string]bool{}
		for _, d := range info.Dimensions {
			seen[d] = true
		}
		for _, d := range c.Dims() {
			if !seen[d.ID] {
				info.Dimensions = append(info.Dimensions, d.ID)
			}
		}
		meta := chartJSON(c, v.db)
		if fe, _ := meta["first_entry"].(int64); fe > 0 && (info.FirstEntry == 0 || fe < info.FirstEntry) {
			info.FirstEntry = fe
		}
		if le, _ := meta["last_entry"].(int64); le > info.LastEntry {
			info.LastEntry = le
		}
	}
	for _, info := range out {
		sort.Strings(info.Charts)
	}
	return map[string]any{"api": api, "node": v.id, "hostname": v.hostname, "contexts_count": len(out), "contexts": out}
}

// writePrometheus exposes the local host and, on a hub, every remote node
// (instance label = hostname, node label = node id).
func (s *Server) writePrometheus(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	seen := map[string]bool{}
	s.writePromCharts(w, s.reg.Host.Hostname, "", s.reg.Charts(), seen)
	if s.opt.Nodes != nil {
		for _, n := range s.opt.Nodes.List() {
			s.writePromCharts(w, n.Host.Hostname, n.ID, n.Registry().Charts(), seen)
		}
	}
}

func (s *Server) writePrometheusFor(w http.ResponseWriter, v *view) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	s.writePromCharts(w, v.hostname, v.id, v.reg.Charts(), map[string]bool{})
}

func (s *Server) writePromCharts(w http.ResponseWriter, host, node string, charts []*registry.Chart, seen map[string]bool) {
	// HELP/TYPE may appear once per metric family, so charts sharing a
	// context (per-core cpu, per-disk io, ...) are grouped by metric name.
	sort.SliceStable(charts, func(i, j int) bool { return charts[i].Context < charts[j].Context })
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
			if node != "" {
				fmt.Fprintf(&lbl, `,node=%q`, node)
			}
			for lk, lv := range c.Labels {
				fmt.Fprintf(&lbl, `,%s=%q`, promName(lk), lv)
			}
			fmt.Fprintf(w, "%s{%s} %g %d\n", metric, lbl.String(), vals[k], ts*1000)
		}
	}
}

func (s *Server) handleCollectors(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"available": collect.Available(), "status": s.sched.Status(), "plugins": s.pluginStatus()})
}

type functionInfo struct {
	Name    string `json:"name"`
	Help    string `json:"help"`
	Timeout int    `json:"timeout"`
}

func (s *Server) handleFunctions(w http.ResponseWriter, r *http.Request) {
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	out := []functionInfo{}
	if v.node != nil {
		for _, f := range v.node.Functions() {
			out = append(out, functionInfo{Name: f.Name, Help: f.Help, Timeout: f.Timeout})
		}
	} else {
		for _, f := range s.functions() {
			out = append(out, functionInfo{Name: f.Name, Help: f.Help, Timeout: f.Timeout})
		}
	}
	writeJSON(w, out)
}

func (s *Server) functions() []collect.Function {
	out := s.sched.Functions()
	return append(out, s.opt.ExtraFunctions...)
}

// handleFunction runs one collector function: ?function=processes plus any
// extra query parameters are passed through as arguments.
func (s *Server) handleFunction(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	name := q.Get("function")
	if name == "" {
		name = q.Get("name")
	}
	args := map[string]string{}
	for k, val := range q {
		if k != "function" && k != "name" && k != "token" && k != "node" && len(val) > 0 {
			args[k] = val[0]
		}
	}
	if v.node != nil { // proxy to the agent over its stream connection
		timeout := 10 * time.Second
		for _, f := range v.node.Functions() {
			if f.Name == name && f.Timeout > 0 {
				timeout = time.Duration(f.Timeout) * time.Second
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		res, err := v.node.Call(ctx, name, args)
		if err != nil {
			code := http.StatusBadGateway
			if err.Error() == "unknown function" {
				code = http.StatusNotFound
			}
			http.Error(w, err.Error(), code)
			return
		}
		writeJSON(w, map[string]any{"function": name, "node": v.id, "time": time.Now().Unix(), "result": json.RawMessage(res)})
		return
	}
	var fn *collect.Function
	for _, f := range s.functions() {
		if f.Name == name {
			fn = &f
			break
		}
	}
	if fn == nil {
		http.Error(w, "unknown function", http.StatusNotFound)
		return
	}
	timeout := time.Duration(fn.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	res, err := fn.Run(ctx, args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"function": fn.Name, "time": time.Now().Unix(), "result": res})
}

func (s *Server) pluginStatus() []plugins.Status {
	if s.opt.Plugins == nil {
		return []plugins.Status{}
	}
	return s.opt.Plugins.Status()
}

func (s *Server) alarmSummary() any {
	if s.opt.Health == nil {
		return nil
	}
	return s.opt.Health.Summary()
}

func (s *Server) handleIngestOpenMetrics(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	samples := ingest.ParseOpenMetrics(string(body))
	n := s.ingest.Apply(s.reg, time.Now(), samples)
	writeJSON(w, map[string]any{"samples": n, "parsed": len(samples)})
}

func (s *Server) handleIngestOTLP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	samples, err := ingest.ParseOTLP(body, r.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	n := s.otlp.Apply(s.reg, time.Now(), samples)
	if strings.HasPrefix(r.URL.Path, "/v1/") {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"partialSuccess":{}}`)
		return
	}
	writeJSON(w, map[string]any{"samples": n, "parsed": len(samples)})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	args := map[string]string{"query": q.Get("query"), "source": q.Get("source"), "after": q.Get("after"), "before": q.Get("before"), "limit": q.Get("limit")}
	if v.node != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		res, err := v.node.Call(ctx, "logs", args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]any{"function": "logs", "node": v.id, "time": time.Now().Unix(), "result": json.RawMessage(res)})
		return
	}
	limit := 200
	if n, _ := strconv.Atoi(q.Get("limit")); n > 0 {
		limit = n
	}
	lq := collect.LogQuery{Source: q.Get("source"), Query: q.Get("query"), Limit: limit}
	if n, _ := strconv.ParseInt(q.Get("after"), 10, 64); n != 0 {
		lq.After = n
	}
	if n, _ := strconv.ParseInt(q.Get("before"), 10, 64); n != 0 {
		lq.Before = n
	}
	rows := collect.QueryLogs(lq)
	tab := collect.Table{Columns: []string{"time", "priority", "unit", "pid", "message"}, Total: len(rows), Rows: make([]any, len(rows))}
	for i, row := range rows {
		tab.Rows[i] = row
	}
	writeJSON(w, map[string]any{"function": "logs", "time": time.Now().Unix(), "result": tab})
}

func (s *Server) handleWeights(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	method := q.Get("method")
	if method == "" {
		method = "anomaly-rate"
	}
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	if method == "ks2" || method == "volume" {
		now := time.Now().Unix()
		before := parseTime(q.Get("before"), now, now)
		after := parseTime(q.Get("after"), before, before-300)
		baseBefore := parseTime(q.Get("baseline_before"), before, after)
		baseAfter := parseTime(q.Get("baseline_after"), baseBefore, baseBefore-600)
		weights := collect.Correlate(v.reg, v.db, method, after, before, baseAfter, baseBefore)
		writeJSON(w, map[string]any{"method": method, "weights": weights, "count": len(weights),
			"after": after, "before": before, "baseline_after": baseAfter, "baseline_before": baseBefore})
		return
	}
	c := s.sched.Collector("ml")
	wp, ok := c.(collect.WeightProvider)
	if !ok || c == nil {
		http.Error(w, "ml collector disabled", http.StatusNotFound)
		return
	}
	weights := wp.Weights(method)
	writeJSON(w, map[string]any{"method": method, "weights": weights, "count": len(weights)})
}
