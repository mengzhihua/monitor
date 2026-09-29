package collect

import (
	"context"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/preprocess"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func testReg(t *testing.T) *registry.Registry {
	t.Helper()
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return registry.New(&registry.Host{ID: "h", Hostname: "h", UpdateEvery: 1}, db)
}

func TestSNMPv3ItemsAndLLD(t *testing.T) {
	s := &snmpCollector{cfg: snmpConfig{
		Address: "10.0.0.5", Version: "3", User: "mon", AuthProto: "SHA", AuthPass: "auth-secret", PrivProto: "AES", PrivPass: "priv-secret",
		Items: []snmpItem{{Name: "load", OID: "1.3.6.1.4.1.1.0", Steps: []preprocess.Step{{Type: "regex", Pattern: `([0-9.]+)`}}}},
		LLD:   []snmpLLD{{Name: "disks", Table: "1.3.6.1.4.1.2.1.2", Value: "1.3.6.1.4.1.2.1.3", Units: "%"}},
	}}
	args, err := s.snmpArgs("1.3.6.1.2.1.1.3.0")
	if err != nil || !containsAll(args, []string{"-v", "3", "-l", "authPriv", "-u", "mon", "-a", "SHA", "-x", "AES"}) {
		t.Fatalf("args %v err %v", args, err)
	}
	if _, err := s.snmpArgs("-bad"); err == nil {
		t.Fatal("accepted a flag-like oid")
	}
	s.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		oid := args[len(args)-1]
		switch oid {
		case "1.3.6.1.2.1.1.3.0":
			return []byte(".1.3.6.1.2.1.1.3.0 = Timeticks: (500) 0:00:05.00\n"), nil
		case "1.3.6.1.2.1.2.2.1.2":
			return []byte(".1.3.6.1.2.1.2.2.1.2.1 = STRING: lo\n"), nil
		case "1.3.6.1.2.1.2.2.1.10", "1.3.6.1.2.1.2.2.1.16":
			return []byte(".1.3.6.1.2.1.2.2.1.10.1 = Counter32: 10\n"), nil
		case "1.3.6.1.2.1.2.2.1.8":
			return []byte(".1.3.6.1.2.1.2.2.1.8.1 = INTEGER: 1\n"), nil
		case "1.3.6.1.4.1.1.0":
			return []byte(`.1.3.6.1.4.1.1.0 = STRING: "load=1.5"` + "\n"), nil
		case "1.3.6.1.4.1.2.1.2":
			return []byte(".1.3.6.1.4.1.2.1.2.1 = STRING: root\n"), nil
		case "1.3.6.1.4.1.2.1.3":
			return []byte(".1.3.6.1.4.1.2.1.3.1 = INTEGER: 40\n"), nil
		default:
			t.Fatalf("oid %s", oid)
			return nil, nil
		}
	}
	reg := testReg(t)
	if err := s.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := s.Collect(context.Background(), reg, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if _, v := mustChart(t, reg, "snmp.item.load").LastValues(); v["value"] != 1.5 {
		t.Fatalf("item %+v", v)
	}
	if ch := mustChart(t, reg, "snmp.lld.disks.1"); ch.Units != "%" {
		t.Fatalf("lld units %s", ch.Units)
	}
	if _, v := mustChart(t, reg, "snmp.lld.disks.1").LastValues(); v["value"] != 40 {
		t.Fatalf("lld value")
	}
}

func TestSSHJolokiaHTTPScenarioDependent(t *testing.T) {
	reg := testReg(t)
	ssh := &sshcheckCollector{cfg: sshcheckConfig{Command: "ssh", Timeout: time.Second, Jobs: []sshJob{{
		Name: "load", Address: "10.1.1.1", User: "mon", Command: "cat /proc/loadavg",
		Steps: []preprocess.Step{{Type: "regex", Pattern: `^([0-9.]+)`}},
	}}}}
	ssh.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "ssh" || args[len(args)-1] != "cat /proc/loadavg" || args[len(args)-2] != "mon@10.1.1.1" {
			t.Fatalf("ssh %s %v", name, args)
		}
		return []byte("0.25 0.20 0.10\n"), nil
	}
	if err := ssh.Init(reg); err != nil {
		t.Fatal(err)
	}
	_ = ssh.Collect(context.Background(), reg, time.Unix(10, 0))
	if _, v := mustChart(t, reg, "sshcheck.load").LastValues(); v["value"] != 0.25 {
		t.Fatalf("ssh %+v", v)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/jolokia/read/java.lang:type=Memory/HeapMemoryUsage/used":
			_, _ = w.Write([]byte(`{"value":80,"status":200}`))
		case "/metrics":
			_, _ = w.Write([]byte("ready_users 7\n"))
		case "/login":
			_, _ = w.Write([]byte("sid=abc"))
		case "/home":
			if c, _ := r.Cookie("sid"); c == nil || c.Value != "abc" {
				http.Error(w, "no", 401)
				return
			}
			_, _ = w.Write([]byte("home"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	j := &jolokiaCollector{}
	if err := j.Configure(func(v any) error {
		*v.(*jolokiaConfig) = jolokiaConfig{Jobs: []jolokiaJob{{Name: "heap", URL: srv.URL + "/jolokia", MBean: "java.lang:type=Memory", Attribute: "HeapMemoryUsage", Path: "used"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := j.Init(reg); err != nil {
		t.Fatal(err)
	}
	_ = j.Collect(context.Background(), reg, time.Unix(11, 0))
	if _, v := mustChart(t, reg, "jolokia.heap").LastValues(); v["value"] != 80 {
		t.Fatalf("jolokia %+v", v)
	}
	h := &httpagentCollector{}
	if err := h.Configure(func(v any) error {
		*v.(*httpagentConfig) = httpagentConfig{Jobs: []httpagentJob{{Name: "users", URL: srv.URL + "/metrics", Steps: []preprocess.Step{{Type: "regex", Pattern: `ready_users ([0-9]+)`}}}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.Init(reg); err != nil {
		t.Fatal(err)
	}
	_ = h.Collect(context.Background(), reg, time.Unix(12, 0))
	if _, v := mustChart(t, reg, "httpagent.users").LastValues(); v["value"] != 7 {
		t.Fatalf("http %+v", v)
	}
	ws := &webscenarioCollector{}
	if err := ws.Configure(func(v any) error {
		*v.(*webscenarioConfig) = webscenarioConfig{Jobs: []webscenarioJob{{Name: "login", Steps: []webscenarioStep{
			{URL: srv.URL + "/login", Extract: `sid=([a-z]+)`, Var: "sid"},
			{URL: srv.URL + "/home", Headers: map[string]string{"Cookie": "sid={{sid}}"}},
		}}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Cookie header is not how the test server reads cookies from the request Cookie header via r.Cookie. Set-Cookie from first response is not automatically stored.
	// The second step sends Cookie: sid={{sid}} which http.Client puts in the Cookie header, and r.Cookie reads it. Good.
	if err := ws.Init(reg); err != nil {
		t.Fatal(err)
	}
	_ = ws.Collect(context.Background(), reg, time.Unix(13, 0))
	if _, v := mustChart(t, reg, "webscenario.status.login").LastValues(); v["success"] != 1 {
		t.Fatalf("scenario %+v", v)
	}

	reg.AddChart(&registry.Chart{ID: "system.ram", Dimensions: []*registry.Dimension{{ID: "used"}}})
	_ = reg.Collect("system.ram", time.Unix(14, 0), map[string]float64{"used": 40})
	dep := &dependentCollector{cfg: dependentConfig{Items: []dependentItem{{Name: "used_x2", Chart: "system.ram", Dimension: "used", Steps: []preprocess.Step{{Type: "multiplier", Factor: 2}}}}}}
	if err := dep.Init(reg); err != nil {
		t.Fatal(err)
	}
	_ = dep.Collect(context.Background(), reg, time.Unix(14, 0))
	if _, v := mustChart(t, reg, "dependent.used_x2").LastValues(); v["value"] != 80 {
		t.Fatalf("dependent %+v", v)
	}
	if math.IsNaN(vOr(reg, "dependent.used_x2")) {
		t.Fatal("missing dependent")
	}
}

func TestNetscanFindsLocalPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	hosts, open := ScanCIDR(context.Background(), "127.0.0.1/32", []int{port}, time.Second)
	if hosts != 1 || open != 1 {
		t.Fatalf("hosts %d open %d", hosts, open)
	}
	if _, err := expandCIDR("10.0.0.0/16"); err == nil {
		t.Fatal("accepted a /16")
	}
}

func mustChart(t *testing.T, reg *registry.Registry, id string) *registry.Chart {
	t.Helper()
	ch, ok := reg.Chart(id)
	if !ok {
		t.Fatalf("missing chart %s", id)
	}
	return ch
}

func containsAll(args, want []string) bool {
	got := map[string]bool{}
	for _, a := range args {
		got[a] = true
	}
	for _, w := range want {
		if !got[w] {
			return false
		}
	}
	return true
}

func vOr(reg *registry.Registry, id string) float64 {
	ch, ok := reg.Chart(id)
	if !ok {
		return math.NaN()
	}
	_, v := ch.LastValues()
	return v["value"]
}
