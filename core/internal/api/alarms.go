package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/health"
)

// GET /api/v1/alarms?all=true&active=true[&node=id]
//
//	{"hostname":"h","status":true,"now":...,"alarms":{"system.ram.ram_in_use":{...}},"summary":{...}}
//
// For a remote node the hub mirrors the agent's transitions, so alarms are
// reconstructed from the latest transition per alarm.
func (s *Server) handleAlarms(w http.ResponseWriter, r *http.Request) {
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	all := r.URL.Query().Get("all") == "true"
	onlyActive := r.URL.Query().Get("active") == "true"
	if v.node != nil {
		out := map[string]health.Alarm{}
		var sum health.Summary
		for _, e := range v.node.Alarms() {
			a := alarmFromEntry(e)
			switch a.Status {
			case health.StatusWarning:
				sum.Warning++
			case health.StatusCritical:
				sum.Critical++
			default:
				sum.Normal++
			}
			if !all && a.Status < health.StatusWarning {
				continue
			}
			out[a.Chart+"."+a.Name] = a
		}
		writeJSON(w, map[string]any{"node": v.id, "hostname": v.hostname, "now": time.Now().Unix(), "summary": sum, "alarms": out})
		return
	}
	if s.opt.Health == nil {
		http.Error(w, "health engine disabled", http.StatusNotFound)
		return
	}
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
		"node":     v.id,
		"hostname": s.reg.Host.Hostname,
		"now":      s.opt.Health.Now().Unix(),
		"summary":  s.opt.Health.Summary(),
		"alarms":   out,
	})
}

func alarmFromEntry(e health.LogEntry) health.Alarm {
	updated := e.Updated
	if updated < e.When {
		updated = e.When
	}
	return health.Alarm{ID: e.AlarmID, Name: e.Name, Chart: e.Chart, Context: e.Context, Family: e.Family, Class: e.Class,
		Type: e.Type, Component: e.Component, Units: e.Units, Info: e.Info, Recipient: e.Recipient, Source: "remote",
		Status: e.Status, Value: e.Value, LastUpdated: updated, LastStatusChange: e.When, Active: true}
}

// GET /api/v1/alarm_log?after=<unique_id>[&node=id]  — entries newer than after (0 = all).
func (s *Server) handleAlarmLog(w http.ResponseWriter, r *http.Request) {
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	var entries []health.LogEntry
	if v.node != nil {
		entries = v.node.AlarmLog(after)
	} else {
		if s.opt.Health == nil {
			http.Error(w, "health engine disabled", http.StatusNotFound)
			return
		}
		entries = s.opt.Health.Log(after)
	}
	if q := r.URL.Query().Get("alarm"); q != "" {
		f := entries[:0]
		for _, e := range entries {
			if e.Name == q || e.Chart+"."+e.Name == q {
				f = append(f, e)
			}
		}
		entries = f
	}
	if entries == nil {
		entries = []health.LogEntry{}
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

func (s *Server) handleAlarmVariables(w http.ResponseWriter, r *http.Request) {
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	chart := r.URL.Query().Get("chart")
	if chart == "" {
		http.Error(w, "chart required", http.StatusBadRequest)
		return
	}
	if _, ok := v.reg.Chart(chart); !ok {
		http.Error(w, "chart not found", http.StatusNotFound)
		return
	}
	vars := map[string]any{}
	if v.node == nil && s.opt.Health != nil {
		vars = s.opt.Health.ChartVariables(chart, r.URL.Query().Get("alarm"))
	} else if c, ok := v.reg.Chart(chart); ok {
		_, last := c.LastValues()
		for k, val := range last {
			vars[k] = val
		}
	}
	writeJSON(w, map[string]any{"chart": chart, "variables": vars})
}

type silenceBody struct {
	All   *bool  `json:"all"`
	Alarm string `json:"alarm"`
	Chart string `json:"chart"`
	Until int64  `json:"until"`
	Clear bool   `json:"clear"`
}

func (s *Server) handleSilence(w http.ResponseWriter, r *http.Request) {
	if s.opt.Health == nil {
		http.Error(w, "health engine disabled", http.StatusNotFound)
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, s.opt.Health.SilenceInfo())
		return
	}
	var body silenceBody
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body)
	}
	q := r.URL.Query()
	if q.Get("all") == "true" {
		t := true
		body.All = &t
	} else if q.Get("all") == "false" {
		f := false
		body.All = &f
	}
	if q.Get("alarm") != "" {
		body.Alarm = q.Get("alarm")
	}
	if q.Get("chart") != "" {
		body.Chart = q.Get("chart")
	}
	if q.Get("clear") == "true" {
		body.Clear = true
	}
	if u := q.Get("until"); u != "" {
		body.Until, _ = strconv.ParseInt(u, 10, 64)
	}
	key := body.Alarm
	if body.Chart != "" && body.Alarm != "" && !strings.Contains(body.Alarm, ".") {
		key = body.Chart + "." + body.Alarm
	}
	s.opt.Health.ApplySilence(body.All, key, body.Until, body.Clear)
	writeJSON(w, s.opt.Health.SilenceInfo())
}

// PublishAlarm pushes a local transition to live WebSocket clients watching
// this host as {"alarm":{...LogEntry}}; wire it via health.Engine.SetOnEvent.
func (s *Server) PublishAlarm(e health.LogEntry) { s.publishAlarm("", e) }

func (s *Server) publishAlarm(nodeID string, e health.LogEntry) {
	b, err := json.Marshal(struct {
		Node  string          `json:"node,omitempty"`
		Alarm health.LogEntry `json:"alarm"`
	}{nodeID, e})
	if err != nil {
		return
	}
	s.live.broadcastAll(nodeID, b)
}
