package sla

import (
	"github.com/mengzhihua/monitor/core/internal/health"
	"testing"
)

func TestAvailabilityPartitionsInstancesAndUnknownHistory(t *testing.T) {
	alarms := []health.Alarm{{Name: "down", Chart: "a"}, {Name: "down", Chart: "b"}, {Name: "missing", Chart: "c", Status: health.StatusClear}}
	log := []health.LogEntry{
		{Name: "down", Chart: "a", When: 0, Status: health.StatusClear},
		{Name: "down", Chart: "b", When: 0, Status: health.StatusWarning},
		{Name: "down", Chart: "a", When: 20, Status: health.StatusWarning},
		{Name: "down", Chart: "b", When: 30, Status: health.StatusClear},
		{Name: "down", Chart: "a", When: 50, Status: health.StatusUndefined},
		{Name: "down", Chart: "a", When: 70, Status: health.StatusClear},
	}
	rows := AlarmAvailability(alarms, log, 0, 100)
	if rows[0].Chart != "a" || rows[0].Raised != 30 || rows[0].Known != 80 || rows[0].Unknown != 20 || rows[0].Uptime != nil {
		t.Fatalf("a: %+v", rows[0])
	}
	if rows[1].Chart != "b" || rows[1].Raised != 30 || rows[1].Uptime == nil || *rows[1].Uptime != 70 {
		t.Fatalf("b: %+v", rows[1])
	}
	if rows[2].Known != 0 || rows[2].Unknown != 100 || rows[2].Uptime != nil {
		t.Fatalf("missing: %+v", rows[2])
	}
}

func TestAvailabilityWindowEdges(t *testing.T) {
	alarms := []health.Alarm{{Name: "x", Chart: "a"}}
	for _, bounds := range [][2]int64{{10, 100}, {100, 10}} {
		rows := AlarmAvailability(alarms, []health.LogEntry{{Name: "x", Chart: "a", When: 0, Status: health.StatusWarning}, {Name: "x", Chart: "a", When: 50, Status: health.StatusClear}, {Name: "x", Chart: "a", When: 200, Status: health.StatusWarning}}, bounds[0], bounds[1])
		if rows[0].Raised != 40 || rows[0].Known != 90 || rows[0].Unknown != 0 {
			t.Fatalf("%+v", rows[0])
		}
	}
	if r := AlarmAvailability(alarms, nil, 10, 10)[0]; r.Uptime != nil || r.Window != 0 {
		t.Fatalf("zero: %+v", r)
	}
	// Chart-less legacy history cannot be attributed to a concrete instance.
	if r := AlarmAvailability(alarms, []health.LogEntry{{Name: "x", When: 0, Status: health.StatusClear}}, 0, 100)[0]; r.Uptime != nil {
		t.Fatal("ambiguous history became healthy")
	}
}

func TestServiceUnknownAndSharedDependencies(t *testing.T) {
	services := []Service{{Name: "empty"}, {Name: "missing", Alarms: []string{"not-found"}}, {Name: "undefined", Alarms: []string{"undefined"}}, {Name: "parent", Children: []string{"empty", "good"}}, {Name: "root", Children: []string{"left", "right"}}, {Name: "left", Children: []string{"good"}}, {Name: "right", Children: []string{"good"}}, {Name: "good", Alarms: []string{"good"}}}
	alarms := []health.Alarm{{Name: "undefined", Status: health.StatusUndefined}, {Name: "good", Status: health.StatusClear}}
	report := Build(services, alarms, []health.LogEntry{{Name: "good", When: 0, Status: health.StatusClear}}, 0, 100)
	for _, i := range []int{0, 1, 2, 3} {
		if n := report.Services[i]; n.Status != "UNKNOWN" || n.SLA != nil {
			t.Fatalf("%+v", n)
		}
	}
	if n := report.Services[4]; n.Status != "CLEAR" || n.SLA == nil || *n.SLA != 100 {
		t.Fatalf("shared dependency: %+v", n)
	}
	cyclic := Build([]Service{{Name: "a", Children: []string{"b"}}, {Name: "b", Children: []string{"a"}}}, nil, nil, 0, 100)
	if cyclic.Services[0].Status != "UNKNOWN" || cyclic.Services[0].SLA != nil {
		t.Fatal("cycle became healthy")
	}
}
