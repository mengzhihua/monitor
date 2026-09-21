package api

import (
	"encoding/json"
	"net/http"
	"strconv"
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
