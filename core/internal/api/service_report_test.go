package api

import (
	"fmt"
	"testing"

	"github.com/mengzhihua/monitor/core/internal/operations"
	"github.com/mengzhihua/monitor/core/internal/sla"
)

func TestServiceReportScopeHistoryAndLimits(t *testing.T) {
	dir := t.TempDir()
	store, err := operations.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 205; i++ {
		_, err := store.Update(fmt.Sprintf("local-%d", i), "operator", "comment", "handover-note", 0, operations.Target{Node: "id", Name: "child_alarm", Chart: "cpu", Severity: "WARNING"})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, target := range []operations.Target{{Node: "remote", Name: "child_alarm", Severity: "WARNING"}, {Node: "id", Name: "unrelated", Severity: "WARNING"}} {
		if _, err := store.Update(target.Node+target.Name, "operator", "comment", "must-not-export", 0, target); err != nil {
			t.Fatal(err)
		}
	}
	services := []sla.Service{{Name: "root", Children: []string{"child"}}, {Name: "child", Alarms: []string{"child_alarm"}}, {Name: "unrelated", Alarms: []string{"unrelated"}}}
	ts, _ := newTestServer(t, Options{OperationsDir: dir, Services: services, Users: []User{{Name: "admin", Token: "a", Role: RoleAdmin}, {Name: "oncall", Token: "t", Role: RoleTroubleshooter}, {Name: "viewer", Token: "v", Role: RoleViewer}}})
	var report serviceHandover
	response := getJSON(t, ts.URL+"/api/v1/services/report?service=root&window=1h&token=t", &report)
	if response.StatusCode != 200 || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("response %d", response.StatusCode)
	}
	if report.Schema != "monitor.service-handover.v1" || len(report.Report.Services) != 2 || report.Revision == "" || report.Report.To-report.Report.From != 3600 {
		t.Fatalf("report: %+v", report)
	}
	if len(report.History) != 200 || report.HistoryMatched != 205 || report.HistoryLimit != 200 || !report.Persistent {
		t.Fatalf("history count %d/%d", len(report.History), report.HistoryMatched)
	}
	for _, h := range report.History {
		if h.Problem.Node != "id" || h.Problem.Name != "child_alarm" {
			t.Fatalf("scope leak: %+v", h)
		}
	}
	for _, tc := range []struct {
		query string
		code  int
	}{{"service=root&token=v", 403}, {"service=missing&token=a", 404}, {"service=root&window=no&token=a", 400}, {"service=root&window=2200h&token=a", 400}, {"service=root&node=remote&token=a", 400}} {
		if r := getJSON(t, ts.URL+"/api/v1/services/report?"+tc.query, nil); r.StatusCode != tc.code {
			t.Fatalf("%s: %d", tc.query, r.StatusCode)
		}
	}
}
func TestServiceClosureDeduplicatesSharedDependencies(t *testing.T) {
	services := []sla.Service{{Name: "root", Children: []string{"a", "b"}}, {Name: "a", Children: []string{"shared"}}, {Name: "b", Children: []string{"shared"}}, {Name: "shared", Alarms: []string{"down"}}}
	selected, alarms, ok := serviceClosure(services, "root")
	if !ok || len(selected) != 4 || len(alarms) != 1 || !alarms["down"] {
		t.Fatalf("%+v %+v", selected, alarms)
	}
}
