package api

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/mengzhihua/monitor/core/internal/audit"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/sla"
)

func TestServicesTopologyAndCommand(t *testing.T) {
	log, err := audit.Open(t.TempDir(), 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	ts, _ := newTestServer(t, Options{
		Token:    "secret",
		Audit:    log,
		Services: []sla.Service{{Name: "edge", Alarms: []string{"ram_high"}}},
		Links:    []sla.Link{{Source: "a", Target: "b"}},
		Commands: []Command{{Name: "echo-ok", Argv: []string{os.Args[0], "-test.run=^$"}}},
		Users:    []User{{Name: "boss", Token: "admin-cmd", Role: RoleAdmin}},
	})
	var report sla.Report
	getJSON(t, ts.URL+"/api/v1/services?token=admin-cmd", &report)
	if len(report.Services) != 1 || report.Services[0].Name != "edge" {
		t.Fatalf("%+v", report)
	}
	var topo struct {
		Edges []struct {
			Source string `json:"source"`
			Target string `json:"target"`
			Kind   string `json:"kind"`
		} `json:"edges"`
	}
	getJSON(t, ts.URL+"/api/v1/topology?token=admin-cmd", &topo)
	if len(topo.Edges) != 1 || topo.Edges[0].Kind != "manual" {
		t.Fatalf("%+v", topo)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/commands", strings.NewReader(`{"name":"echo-ok"}`))
	req.Header.Set("Authorization", "Bearer admin-cmd")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("command %d %s", resp.StatusCode, body)
	}
	var got struct {
		Name       string `json:"name"`
		Exit       int    `json:"exit"`
		DurationMs int64  `json:"duration_ms"`
		Output     string `json:"output"`
	}
	if err := json.Unmarshal(body, &got); err != nil || got.Name != "echo-ok" || got.DurationMs < 0 {
		t.Fatalf("%s err=%v", body, err)
	}
	entries := log.Query(0, 20, "boss")
	found := false
	for _, e := range entries {
		if e.Action == "command:echo-ok" && e.Command == "echo-ok" && e.ExitCode != nil && e.Argv != "" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("audit missing command run: %+v", entries)
	}
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/v1/discovery/scan", strings.NewReader(`{"cidr":"10.0.0.0/8","ports":[22]}`))
	req.Header.Set("Authorization", "Bearer admin-cmd")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("scan %d", resp.StatusCode)
	}
	_ = health.StatusClear
}
