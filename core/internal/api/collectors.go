package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/plugins"
)

func (s *Server) handleCollectors(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"available":           collect.Available(),
		"status":              s.sched.Status(),
		"plugins":             s.pluginStatus(),
		"prometheus_profiles": collect.PromProfileNames(),
	})
}

func (s *Server) handlePrometheusCatalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, collect.PromCatalog())
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
	var out []collect.Function
	if s.sched != nil {
		out = s.sched.Functions()
	}
	out = append(out, s.opt.ExtraFunctions...)
	if src, ok := s.opt.Plugins.(interface {
		Functions() []plugins.FunctionSpec
	}); ok {
		for _, fn := range src.Functions() {
			out = append(out, collect.Function{Name: fn.Name, Help: fn.Help, Timeout: fn.Timeout, Run: fn.Run})
		}
	}
	return out
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

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	args := map[string]string{}
	for _, key := range []string{"query", "source", "after", "before", "limit", "channel", "unit", "priority", "boot", "cursor", "xpath"} {
		args[key] = q.Get(key)
	}
	if v.node != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		res, err := v.node.Call(ctx, "logs", args)
		if err != nil {
			if err.Error() == "unknown function" {
				// The agent's logs collector is disabled (trimmed deployments):
				// surface that instead of a bare gateway error.
				http.Error(w, "node has no logs function (logs collector disabled on the agent)", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]any{"function": "logs", "node": v.id, "time": time.Now().Unix(), "result": json.RawMessage(res)})
		return
	}
	// Reuse the collector's live buffer and configured sources. The standalone
	// query remains available when the logs collector is not enabled.
	for _, fn := range s.functions() {
		if fn.Name != "logs" {
			continue
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		res, err := fn.Run(ctx, args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"function": "logs", "time": time.Now().Unix(), "result": res})
		return
	}
	limit := 200
	if n, _ := strconv.Atoi(q.Get("limit")); n > 0 {
		limit = min(n, 1000)
	}
	lq := collect.LogQuery{Source: q.Get("source"), Query: q.Get("query"), Channel: q.Get("channel"), Limit: limit, Unit: q.Get("unit"), Priority: q.Get("priority"), Boot: q.Get("boot"), Cursor: q.Get("cursor"), XPath: q.Get("xpath")}
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
