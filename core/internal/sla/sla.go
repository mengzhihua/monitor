// Package sla computes service-tree status and availability from alarm history.
package sla

import (
	"sort"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/health"
)

// Service is one node of the service tree. Children are other service names.
// Alarms are health alarm names that feed this service directly.
type Service struct {
	Name     string   `yaml:"name" json:"name"`
	Alarms   []string `yaml:"alarms,omitempty" json:"alarms,omitempty"`
	Children []string `yaml:"children,omitempty" json:"children,omitempty"`
}

// Link is a manual topology edge. LLDP neighbors are added by the API.
type Link struct {
	Source string `yaml:"source" json:"source"`
	Target string `yaml:"target" json:"target"`
}

// Node is a service with its current status and SLA over the requested window.
type Node struct {
	Name     string   `json:"name"`
	Status   string   `json:"status"`
	SLA      float64  `json:"sla"`
	Alarms   []string `json:"alarms,omitempty"`
	Children []string `json:"children,omitempty"`
}

// Report is the service tree plus the window that was measured.
type Report struct {
	From     int64  `json:"from"`
	To       int64  `json:"to"`
	Services []Node `json:"services"`
}

// Availability is one alarm's time-not-raised percentage.
type Availability struct {
	Name   string  `json:"name"`
	Chart  string  `json:"chart"`
	Uptime float64 `json:"uptime"`
	Raised int64   `json:"raised_seconds"`
	Window int64   `json:"window_seconds"`
}

// Build walks services in definition order. Status is the worst of the
// service's own alarms and its children. SLA is the minimum of those inputs.
func Build(services []Service, alarms []health.Alarm, log []health.LogEntry, from, to int64) Report {
	if to < from {
		from, to = to, from
	}
	avail := availabilityByName(log, alarms, from, to)
	status := map[string]string{}
	for _, a := range alarms {
		if a.Status == health.StatusCritical || a.Status == health.StatusWarning {
			cur := status[a.Name]
			if rank(a.Status.String()) > rank(cur) {
				status[a.Name] = a.Status.String()
			}
		}
	}
	byName := map[string]Service{}
	for _, s := range services {
		byName[s.Name] = s
	}
	var visit func(name string, seen map[string]bool) (string, float64)
	visit = func(name string, seen map[string]bool) (string, float64) {
		if seen[name] {
			return "CLEAR", 100
		}
		seen[name] = true
		svc := byName[name]
		st := "CLEAR"
		sla := 100.0
		for _, alarm := range svc.Alarms {
			if rank(status[alarm]) > rank(st) {
				st = status[alarm]
			}
			if v, ok := avail[alarm]; ok && v < sla {
				sla = v
			}
		}
		for _, child := range svc.Children {
			cs, cv := visit(child, seen)
			if rank(cs) > rank(st) {
				st = cs
			}
			if cv < sla {
				sla = cv
			}
		}
		return st, sla
	}
	out := Report{From: from, To: to, Services: []Node{}}
	for _, s := range services {
		st, sla := visit(s.Name, map[string]bool{})
		if st == "" {
			st = "CLEAR"
		}
		out.Services = append(out.Services, Node{Name: s.Name, Status: st, SLA: sla, Alarms: s.Alarms, Children: s.Children})
	}
	return out
}

// AlarmAvailability returns one row per alarm in the snapshot.
func AlarmAvailability(alarms []health.Alarm, log []health.LogEntry, from, to int64) []Availability {
	if to < from {
		from, to = to, from
	}
	window := to - from
	if window <= 0 {
		window = 1
	}
	raised := raisedSeconds(log, from, to)
	logged := map[string]bool{}
	for _, e := range log {
		logged[e.Name] = true
	}
	out := make([]Availability, 0, len(alarms))
	seen := map[string]bool{}
	for _, a := range alarms {
		if seen[a.Name] {
			continue
		}
		seen[a.Name] = true
		sec := raised[a.Name]
		if sec == 0 && !logged[a.Name] && (a.Status == health.StatusWarning || a.Status == health.StatusCritical) {
			sec = window
		}
		if sec > window {
			sec = window
		}
		out = append(out, Availability{
			Name: a.Name, Chart: a.Chart,
			Uptime: float64(window-sec) / float64(window) * 100,
			Raised: sec, Window: window,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func availabilityByName(log []health.LogEntry, alarms []health.Alarm, from, to int64) map[string]float64 {
	rows := AlarmAvailability(alarms, log, from, to)
	out := map[string]float64{}
	for _, row := range rows {
		out[row.Name] = row.Uptime
	}
	for _, e := range log {
		if _, ok := out[e.Name]; !ok {
			out[e.Name] = 100
		}
	}
	return out
}

func raisedSeconds(log []health.LogEntry, from, to int64) map[string]int64 {
	type state struct {
		status health.Status
		at     int64
	}
	entries := append([]health.LogEntry(nil), log...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].When < entries[j].When })
	cur := map[string]state{}
	out := map[string]int64{}
	for _, e := range entries {
		if e.When < from {
			cur[e.Name] = state{status: e.Status, at: from}
			continue
		}
		if e.When > to {
			break
		}
		prev, ok := cur[e.Name]
		if ok && (prev.status == health.StatusWarning || prev.status == health.StatusCritical) {
			out[e.Name] += e.When - prev.at
		}
		start := e.When
		if start < from {
			start = from
		}
		cur[e.Name] = state{status: e.Status, at: start}
	}
	for name, prev := range cur {
		if prev.status == health.StatusWarning || prev.status == health.StatusCritical {
			out[name] += to - prev.at
		}
	}
	return out
}

func rank(status string) int {
	switch strings.ToUpper(status) {
	case "CRITICAL":
		return 3
	case "WARNING":
		return 2
	case "CLEAR":
		return 1
	default:
		return 0
	}
}

// Window resolves a duration ending at now. Empty means 24h.
func Window(now time.Time, raw string) (from, to int64) {
	to = now.Unix()
	d := 24 * time.Hour
	if raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 && parsed <= 90*24*time.Hour {
			d = parsed
		}
	}
	return now.Add(-d).Unix(), to
}
