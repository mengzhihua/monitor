package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/mengzhihua/monitor/core/internal/health"
)

// GET /api/v1/alarms?all=true&active=true
//
//	{"hostname":"h","status":true,"now":...,"alarms":{"system.ram.ram_in_use":{...}},"summary":{...}}
func (s *Server) handleAlarms(w http.ResponseWriter, r *http.Request) {
	if s.opt.Health == nil {
		http.Error(w, "health engine disabled", http.StatusNotFound)
		return
	}
	all := r.URL.Query().Get("all") == "true"
	onlyActive := r.URL.Query().Get("active") == "true"
	alarms := s.opt.Health.Alarms()
	out := make(map[string]health.Alarm, len(alarms))
	for _, a := range alarms {
		if !all && a.Status < health.StatusWarning {
			continue
		}
		if onlyActive && !a.Active {
			continue
		}
		out[a.Chart+"."+a.Name] = a
	}
	writeJSON(w, map[string]any{
		"hostname": s.reg.Host.Hostname,
		"now":      s.opt.Health.Now().Unix(),
		"summary":  s.opt.Health.Summary(),
		"alarms":   out,
	})
}

// GET /api/v1/alarm_log?after=<unique_id>  — entries newer than after (0 = all).
func (s *Server) handleAlarmLog(w http.ResponseWriter, r *http.Request) {
	if s.opt.Health == nil {
		http.Error(w, "health engine disabled", http.StatusNotFound)
		return
	}
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	entries := s.opt.Health.Log(after)
	if q := r.URL.Query().Get("alarm"); q != "" {
		f := entries[:0]
		for _, e := range entries {
			if e.Name == q || e.Chart+"."+e.Name == q {
				f = append(f, e)
			}
		}
		entries = f
	}
	writeJSON(w, entries)
}

// GET /api/v1/alarm_rules — the compiled rule set (for debugging / UI).
func (s *Server) handleAlarmRules(w http.ResponseWriter, r *http.Request) {
	if s.opt.Health == nil {
		http.Error(w, "health engine disabled", http.StatusNotFound)
		return
	}
	rules := s.opt.Health.Rules()
	out := make([]map[string]any, 0, len(rules))
	for _, ru := range rules {
		out = append(out, map[string]any{"source": ru.Source, "spec": ru.Spec, "every": ru.Every.Seconds()})
	}
	writeJSON(w, out)
}

// PublishAlarm pushes a transition to all live WebSocket clients as
// {"alarm":{...LogEntry}}; wire it via health.Engine.SetOnEvent.
func (s *Server) PublishAlarm(e health.LogEntry) {
	b, err := json.Marshal(struct {
		Alarm health.LogEntry `json:"alarm"`
	}{e})
	if err != nil {
		return
	}
	s.live.broadcastAll(b)
}
