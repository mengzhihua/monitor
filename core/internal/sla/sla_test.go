package sla

import (
	"testing"

	"github.com/mengzhihua/monitor/core/internal/health"
)

func TestServiceTreeUsesWorstChildAndUptime(t *testing.T) {
	alarms := []health.Alarm{
		{Name: "web", Chart: "http.check", Status: health.StatusWarning},
		{Name: "db", Chart: "db.up", Status: health.StatusClear},
	}
	log := []health.LogEntry{
		{Name: "web", Status: health.StatusWarning, When: 50},
		{Name: "web", Status: health.StatusClear, When: 80},
		{Name: "db", Status: health.StatusClear, When: 10},
	}
	report := Build([]Service{
		{Name: "edge", Children: []string{"site"}},
		{Name: "site", Alarms: []string{"web", "db"}, Children: nil},
	}, alarms, log, 0, 100)
	if len(report.Services) != 2 || report.Services[0].Status != "WARNING" || report.Services[0].SLA >= 100 {
		t.Fatalf("%+v", report.Services)
	}
	rows := AlarmAvailability(alarms, log, 0, 100)
	var web Availability
	for _, row := range rows {
		if row.Name == "web" {
			web = row
		}
	}
	if web.Raised != 30 || web.Uptime != 70 {
		t.Fatalf("%+v", web)
	}
}

func TestServiceStatusRules(t *testing.T) {
	alarms := []health.Alarm{
		{Name: "web", Status: health.StatusWarning},
		{Name: "db", Status: health.StatusCritical},
		{Name: "edge_down", Status: health.StatusCritical},
	}
	services := []Service{
		{Name: "edge", Children: []string{"web_svc", "db_svc"}, StatusRules: []StatusRule{
			{Type: "children_at_least", N: 2, AtLeast: "WARNING", Status: "CRITICAL"},
			{Type: "ok"},
		}},
		{Name: "half", Children: []string{"web_svc", "db_svc"}, StatusRules: []StatusRule{
			{Type: "children_percent", N: 50, AtLeast: "CRITICAL", Status: "WARNING"},
			{Type: "ok"},
		}},
		{Name: "db_only", Children: []string{"web_svc", "db_svc"}, StatusRules: []StatusRule{
			{Type: "child", Child: "db_svc", AtLeast: "CRITICAL", Status: "CRITICAL"},
			{Type: "ok"},
		}},
		{Name: "own", Alarms: []string{"edge_down"}, Children: []string{"web_svc"}, StatusRules: []StatusRule{
			{Type: "ok"},
		}},
		{Name: "web_svc", Alarms: []string{"web"}},
		{Name: "db_svc", Alarms: []string{"db"}},
	}
	report := Build(services, alarms, nil, 0, 100)
	got := map[string]string{}
	for _, node := range report.Services {
		got[node.Name] = node.Status
	}
	want := map[string]string{
		"edge": "CRITICAL", "half": "WARNING", "db_only": "CRITICAL",
		"own": "CRITICAL", "web_svc": "WARNING", "db_svc": "CRITICAL",
	}
	for name, status := range want {
		if got[name] != status {
			t.Fatalf("%s=%s want %s (%v)", name, got[name], status, got)
		}
	}
	quiet := Build([]Service{{
		Name: "edge", Children: []string{"web_svc"},
		StatusRules: []StatusRule{{Type: "children_at_least", N: 2, AtLeast: "WARNING", Status: "CRITICAL"}, {Type: "ok"}},
	}, {Name: "web_svc", Alarms: []string{"web"}}}, alarms, nil, 0, 100)
	if quiet.Services[0].Status != "CLEAR" {
		t.Fatalf("one warning child should stay clear, got %s", quiet.Services[0].Status)
	}
}

func TestServiceStatusRuleValidation(t *testing.T) {
	ok := []Service{{Name: "edge", Children: []string{"site"}, StatusRules: []StatusRule{
		{Type: "children_at_least", N: 1, AtLeast: "CRITICAL", Status: "CRITICAL"},
		{Type: "ok"},
	}}}
	if err := Validate(ok); err != nil {
		t.Fatal(err)
	}
	bad := []Service{{Name: "edge", Children: []string{"site"}, StatusRules: []StatusRule{
		{Type: "children_percent", N: 0, Status: "CRITICAL"},
	}}}
	if err := Validate(bad); err == nil {
		t.Fatal("accepted a zero percent")
	}
	unknown := []Service{{Name: "edge", StatusRules: []StatusRule{{Type: "average"}}}}
	if err := Validate(unknown); err == nil {
		t.Fatal("accepted an unknown rule")
	}
	missing := []Service{{Name: "edge", Children: []string{"site"}, StatusRules: []StatusRule{
		{Type: "child", Child: "other", Status: "WARNING"},
	}}}
	if err := Validate(missing); err == nil {
		t.Fatal("accepted a child outside the service")
	}
}
