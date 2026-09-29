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
