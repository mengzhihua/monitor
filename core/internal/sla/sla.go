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
	SLA      *float64 `json:"sla"`
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
	Name    string   `json:"name"`
	Chart   string   `json:"chart"`
	Uptime  *float64 `json:"uptime"`
	Raised  int64    `json:"raised_seconds"`
	Window  int64    `json:"window_seconds"`
	Known   int64    `json:"known_seconds"`
	Unknown int64    `json:"unknown_seconds"`
}

// Build walks services in definition order. Without status rules, status is
// the worst of the service's own alarms and its children. With rules, the
// first match sets the child contribution and the service's own alarms can
// still raise it. SLA stays the minimum of those inputs.
func Build(services []Service, alarms []health.Alarm, log []health.LogEntry, from, to int64) Report {
	if to < from {
		from, to = to, from
	}
	rows := AlarmAvailability(alarms, log, from, to)
	avail := map[string]*float64{}
	for _, row := range rows {
		old, exists := avail[row.Name]
		if !exists || (old != nil && (row.Uptime == nil || *row.Uptime < *old)) {
			avail[row.Name] = row.Uptime
		}
	}
	status := map[string]string{}
	for _, a := range alarms {
		st := a.Status.String()
		if a.Status != health.StatusClear && a.Status != health.StatusWarning && a.Status != health.StatusCritical {
			st = "UNKNOWN"
		}
		if rank(st) > rank(status[a.Name]) {
			status[a.Name] = st
		}
	}
	byName := map[string]Service{}
	for _, s := range services {
		byName[s.Name] = s
	}
	var visit func(string, map[string]bool) (string, *float64)
	visit = func(name string, path map[string]bool) (string, *float64) {
		svc, exists := byName[name]
		if path[name] || !exists {
			return "UNKNOWN", nil
		}
		path[name] = true
		defer delete(path, name) // A shared dependency is not a cycle.
		own := "CLEAR"
		value := 100.0
		complete := len(svc.Alarms)+len(svc.Children) > 0
		unknown := !complete
		for _, alarm := range svc.Alarms {
			st := status[alarm]
			if st == "" {
				st = "UNKNOWN"
			}
			if st == "UNKNOWN" {
				unknown = true
			}
			if rank(st) > rank(own) {
				own = st
			}
			v := avail[alarm]
			if v == nil {
				complete = false
			} else if *v < value {
				value = *v
			}
		}
		childStatus := map[string]string{}
		for _, child := range svc.Children {
			st, v := visit(child, path)
			childStatus[child] = st
			if st == "UNKNOWN" {
				unknown = true
			}
			if v == nil {
				complete = false
			} else if *v < value {
				value = *v
			}
		}
		st := combineStatus(own, svc, childStatus)
		if unknown && rank(st) < rank("UNKNOWN") {
			st = "UNKNOWN"
		}
		if !complete {
			return st, nil
		}
		return st, &value
	}
	out := Report{From: from, To: to, Services: []Node{}}
	for _, s := range services {
		st, value := visit(s.Name, map[string]bool{})
		out.Services = append(out.Services, Node{Name: s.Name, Status: st, SLA: value, Alarms: s.Alarms, Children: s.Children})
	}
	return out
}

// AlarmAvailability partitions the requested window using retained state transitions.
// Missing history and undefined/removed states are unknown, never healthy. Current
// state alone cannot establish how long a state held. A percentage is returned only
// when the whole window has defined history. This is log coverage, not probe uptime.
func AlarmAvailability(alarms []health.Alarm, log []health.LogEntry, from, to int64) []Availability {
	if to < from {
		from, to = to, from
	}
	type key struct{ name, chart string }
	entries := map[key][]health.LogEntry{}
	for _, e := range log {
		k := key{e.Name, e.Chart}
		entries[k] = append(entries[k], e)
	}
	seen := map[key]bool{}
	out := make([]Availability, 0, len(alarms))
	for _, a := range alarms {
		k := key{a.Name, a.Chart}
		if seen[k] {
			continue
		}
		seen[k] = true
		events := entries[k]
		sort.SliceStable(events, func(i, j int) bool { return events[i].When < events[j].When })
		row := Availability{Name: a.Name, Chart: a.Chart, Window: to - from}
		at, state := from, health.StatusUndefined
		add := func(end int64) {
			seconds := end - at
			if seconds > 0 && (state == health.StatusClear || state == health.StatusWarning || state == health.StatusCritical) {
				row.Known += seconds
				if state != health.StatusClear {
					row.Raised += seconds
				}
			}
			at = end
		}
		for _, e := range events {
			if e.When > to {
				break
			}
			if e.When <= from {
				state = e.Status
				continue
			}
			add(e.When)
			state = e.Status
		}
		add(to)
		row.Unknown = row.Window - row.Known
		if row.Window > 0 && row.Unknown == 0 {
			value := float64(row.Window-row.Raised) / float64(row.Window) * 100
			row.Uptime = &value
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Chart < out[j].Chart
	})
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
		return 4
	case "WARNING":
		return 3
	case "UNKNOWN":
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
