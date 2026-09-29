package api

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/sla"
)

func TestServicesTopologyAndCommand(t *testing.T) {
	ts, _ := newTestServer(t, Options{
		Token:    "secret",
		Services: []sla.Service{{Name: "edge", Alarms: []string{"ram_high"}}},
		Links:    []sla.Link{{Source: "a", Target: "b"}},
		Commands: []Command{{Name: "echo-ok", Argv: []string{os.Args[0], "-test.run=^$"}}},
	})
	var report sla.Report
	getJSON(t, ts.URL+"/api/v1/services?token=secret", &report)
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
	getJSON(t, ts.URL+"/api/v1/topology?token=secret", &topo)
	if len(topo.Edges) != 1 || topo.Edges[0].Kind != "manual" {
		t.Fatalf("%+v", topo)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/commands?token=secret", strings.NewReader(`{"name":"echo-ok"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("command %d", resp.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/v1/discovery/scan?token=secret", strings.NewReader(`{"cidr":"10.0.0.0/8","ports":[22]}`))
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
