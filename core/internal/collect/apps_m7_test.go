package collect

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestHAProxyCSV(t *testing.T) {
	csv := `# pxname,svname,qcur,scur,smax,slim,stot,bin,bout,ereq,econ,eresp,status,weight,act,bck,type,hrsp_1xx,hrsp_2xx,hrsp_3xx,hrsp_4xx,hrsp_5xx,hrsp_other
http,FRONTEND,,2,10,100,50,200,300,1,0,0,OPEN,,,0,0,0,10,0,2,0,0
http,BACKEND,3,1,2,100,40,200,300,0,0,0,UP,1,2,1,1,0,10,0,2,1,0
`
	rows, err := parseHAProxyCSV(csv)
	if err != nil || len(rows) != 2 {
		t.Fatalf("%v %v", rows, err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(csv))
	}))
	defer srv.Close()
	h := &haproxyCollector{cfg: haproxyConfig{URL: srv.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := h.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := h.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestLighttpdStatus(t *testing.T) {
	body := "Total Accesses: 9\nTotal kBytes: 4\nUptime: 100\nBusyServers: 2\nIdleServers: 6\nScoreboard: .WrC\n"
	st, err := parseLighttpdStatus(body)
	if err != nil || st.num["BusyServers"] != 2 {
		t.Fatalf("%v %v", st, err)
	}
	sb := parseLighttpdScoreboard(st.scoreboard)
	if sb["waiting"] != 1 || sb["write"] != 1 {
		t.Fatalf("%v", sb)
	}
	if _, err := parseLighttpdStatus("BusyWorkers: 1"); err == nil {
		t.Fatal("apache body should fail")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	l := &lighttpdCollector{cfg: lighttpdConfig{URL: srv.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := l.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := l.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestConsulCollector(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health/state/any", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]string{{"Status": "passing"}, {"Status": "critical"}})
	})
	mux.HandleFunc("/v1/agent/metrics", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"Gauges": []map[string]any{
			{"Name": "consul.autopilot.healthy", "Value": 1},
			{"Name": "consul.raft.peers", "Value": 3},
			{"Name": "consul.runtime.alloc_bytes", "Value": 1 << 20},
			{"Name": "consul.serf.lan.members", "Value": 3},
		}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := &consulCollector{cfg: consulConfig{URL: srv.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := c.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestParseWhoisExpiry(t *testing.T) {
	t0, err := parseWhoisExpiry("Registry Expiry Date: 2030-01-02T03:04:05Z\n")
	if err != nil || t0.Year() != 2030 || t0.Month() != 1 || t0.Day() != 2 {
		t.Fatalf("%v %v", t0, err)
	}
	if _, err := parseWhoisExpiry("no dates here"); err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(whoisServer("example.com"), "verisign") {
		t.Fatal(whoisServer("example.com"))
	}
}
