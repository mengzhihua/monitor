// Package sla computes service-tree status and availability from alarm history.
package sla

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/health"
)

// Service is one node of the service tree. Children are other service names.
// Alarms are health alarm names that feed this service directly.
// StatusRules, when set, decide how child services change this service.
// The first matching rule wins. With no rules, status is the worst of the
// service's own alarms and its children.
type Service struct {
	Name        string       `yaml:"name" json:"name"`
	Alarms      []string     `yaml:"alarms,omitempty" json:"alarms,omitempty"`
	Children    []string     `yaml:"children,omitempty" json:"children,omitempty"`
	StatusRules []StatusRule `yaml:"status_rules,omitempty" json:"status_rules,omitempty"`
}

// StatusRule is one Zabbix-style service status calculation rule.
// Type is ok, problem, children_at_least, children_percent, or child.
type StatusRule struct {
	Type    string `yaml:"type" json:"type"`
	N       int    `yaml:"n,omitempty" json:"n,omitempty"`
	AtLeast string `yaml:"at_least,omitempty" json:"at_least,omitempty"`
	Status  string `yaml:"status,omitempty" json:"status,omitempty"`
	Child   string `yaml:"child,omitempty" json:"child,omitempty"`
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

// Build walks services in definition order. Without status rules, status is
// the worst of the service's own alarms and its children. With rules, the
// first match sets the child contribution and the service's own alarms can
// still raise it. SLA stays the minimum of those inputs.
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
		own := "CLEAR"
		sla := 100.0
		for _, alarm := range svc.Alarms {
			if rank(status[alarm]) > rank(own) {
				own = status[alarm]
			}
			if v, ok := avail[alarm]; ok && v < sla {
				sla = v
			}
		}
		childStatus := make(map[string]string, len(svc.Children))
		for _, child := range svc.Children {
			cs, cv := visit(child, seen)
			childStatus[child] = cs
			if cv < sla {
				sla = cv
			}
		}
		return combineStatus(own, svc, childStatus), sla
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

func combineStatus(own string, svc Service, childStatus map[string]string) string {
	if len(svc.StatusRules) == 0 {
		st := own
		for _, cs := range childStatus {
			if rank(cs) > rank(st) {
				st = cs
			}
		}
		if st == "" {
			return "CLEAR"
		}
		return st
	}
	st := own
	for _, rule := range svc.StatusRules {
		got, ok := matchStatusRule(rule, childStatus)
		if !ok {
			continue
		}
		if rank(got) > rank(st) {
			st = got
		}
		break
	}
	if st == "" {
		return "CLEAR"
	}
	return st
}

func matchStatusRule(rule StatusRule, childStatus map[string]string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(rule.Type)) {
	case "ok":
		return "CLEAR", true
	case "problem":
		status, ok := problemStatus(rule.Status)
		return status, ok
	case "children_at_least":
		if countAtLeast(childStatus, rule.AtLeast) >= rule.N {
			return problemStatus(rule.Status)
		}
	case "children_percent":
		total := len(childStatus)
		if total > 0 && countAtLeast(childStatus, rule.AtLeast)*100 >= rule.N*total {
			return problemStatus(rule.Status)
		}
	case "child":
		if rank(childStatus[rule.Child]) >= rank(ruleFloor(rule.AtLeast)) {
			return problemStatus(rule.Status)
		}
	}
	return "", false
}

func countAtLeast(childStatus map[string]string, floor string) int {
	n := 0
	need := rank(ruleFloor(floor))
	for _, st := range childStatus {
		if rank(st) >= need {
			n++
		}
	}
	return n
}

func ruleFloor(status string) string {
	if strings.EqualFold(strings.TrimSpace(status), "CRITICAL") {
		return "CRITICAL"
	}
	return "WARNING"
}

func problemStatus(status string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "WARNING":
		return "WARNING", true
	case "CRITICAL":
		return "CRITICAL", true
	default:
		return "", false
	}
}

// Validate rejects status rules that cannot be evaluated.
func Validate(services []Service) error {
	for _, svc := range services {
		if len(svc.StatusRules) > 8 {
			return fmt.Errorf("service %s: at most 8 status rules", svc.Name)
		}
		children := map[string]bool{}
		for _, child := range svc.Children {
			children[child] = true
		}
		for i, rule := range svc.StatusRules {
			if err := validStatusRule(rule, children); err != nil {
				return fmt.Errorf("service %s rule %d: %w", svc.Name, i+1, err)
			}
		}
	}
	return nil
}

func validStatusRule(rule StatusRule, children map[string]bool) error {
	switch strings.ToLower(strings.TrimSpace(rule.Type)) {
	case "ok":
		return nil
	case "problem":
		if _, ok := problemStatus(rule.Status); !ok {
			return fmt.Errorf("problem status must be WARNING or CRITICAL")
		}
	case "children_at_least":
		if rule.N < 1 || rule.N > 1000 {
			return fmt.Errorf("n must be from 1 to 1000")
		}
		if err := validFloorAndStatus(rule); err != nil {
			return err
		}
	case "children_percent":
		if rule.N < 1 || rule.N > 100 {
			return fmt.Errorf("percent must be from 1 to 100")
		}
		if err := validFloorAndStatus(rule); err != nil {
			return err
		}
	case "child":
		if rule.Child == "" || !children[rule.Child] {
			return fmt.Errorf("child must be one of this service's children")
		}
		if err := validFloorAndStatus(rule); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown type %q", rule.Type)
	}
	return nil
}

func validFloorAndStatus(rule StatusRule) error {
	floor := strings.ToUpper(strings.TrimSpace(rule.AtLeast))
	if floor != "" && floor != "WARNING" && floor != "CRITICAL" {
		return fmt.Errorf("at_least must be WARNING or CRITICAL")
	}
	if _, ok := problemStatus(rule.Status); !ok {
		return fmt.Errorf("status must be WARNING or CRITICAL")
	}
	return nil
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
