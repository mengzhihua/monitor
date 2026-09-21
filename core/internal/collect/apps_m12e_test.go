package collect

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestM12ECollectorsRegistered(t *testing.T) {
	for _, name := range []string{
		"storcli", "nginxvts", "tengine", "nsd", "dnsdist", "dnsmasq_dhcp",
		"isc_dhcpd", "puppet", "openvpn_status_log", "rethinkdb", "yugabytedb", "vernemq",
	} {
		found := false
		for _, n := range Available() {
			if n == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("collector %s not registered", name)
		}
	}
}

func TestStorcliAndNSD(t *testing.T) {
	s := &storcliCollector{
		cfg: storcliConfig{Command: "storcli", Timeout: time.Second},
		run: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(`{"Controllers":[{"Command Status":{"Status":"Success","Controller":0},"Response Data":{"Basics":{"Controller":0},"Status":{"Controller Status":"Optimal"}}}]}`), nil
		},
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := s.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := s.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("storcli.controller_health_status.0"); !ok {
		t.Fatal("missing storcli chart")
	}

	n := &nsdCollector{
		cfg: nsdConfig{Command: "nsd-control", Timeout: time.Second},
		run: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("num.queries=10\nnum.udp=8\nnum.tcp=2\nnum.udp6=0\nnum.tcp6=0\nnum.tls=0\nnum.tls6=0\nnum.rxerr=0\nnum.txerr=0\nnum.dropped=1\nzone.master=2\nzone.slave=0\ntime.boot=99\n"), nil
		},
	}
	if err := n.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := n.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestNginxVTSTengineDNSDist(t *testing.T) {
	vts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"hostName":"h","loadMsec":1000,"nowMsec":6000,"connections":{"active":2,"reading":0,"writing":1,"waiting":1,"accepted":10,"handled":10,"requests":20},"sharedZones":{"maxSize":1000,"usedSize":100},"serverZones":{"*":{"inBytes":50,"outBytes":80,"responses":{"1xx":0,"2xx":18,"3xx":0,"4xx":1,"5xx":1}}}}`))
	}))
	defer vts.Close()
	n := &nginxvtsCollector{cfg: nginxvtsConfig{URL: vts.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := n.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := n.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("www.example.com,127.0.0.1:80,162,6242,1,1,1,0,0,0,0,10,1,10,1,1,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0\n"))
	}))
	defer tg.Close()
	te := &tengineCollector{cfg: tengineConfig{URL: tg.URL, Timeout: time.Second}}
	if err := te.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := te.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	dd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"queries":10,"rdqueries":4,"empty-queries":0,"rule-drop":1,"dyn-blocked":0,"no-policy":0,"noncompliant-queries":0,"self-answered":2,"rule-nxdomain":0,"rule-refused":0,"cache-hits":5,"cache-misses":5,"downstream-timeouts":0,"servfail-responses":0,"real-memory-usage":1048576}`))
	}))
	defer dd.Close()
	d := &dnsdistCollector{cfg: dnsdistConfig{URL: dd.URL, Timeout: time.Second}}
	if err := d.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := d.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestDHCPAndOpenVPNLog(t *testing.T) {
	leases := "1700000000 aa:bb:cc:dd:ee:ff 192.168.1.10 host *\n1700000001 11:22:33:44:55:66 2001:db8::1 host6 *\n"
	d := &dnsmasqDHCPCollector{
		cfg: dnsmasqDHCPConfig{LeasesPath: "/tmp/dnsmasq.leases", ConfPath: "/tmp/dnsmasq.conf"},
		readFile: func(path string) ([]byte, error) {
			if path == "/tmp/dnsmasq.conf" {
				return []byte("dhcp-range=192.168.1.50,192.168.1.150,12h\n"), nil
			}
			return []byte(leases), nil
		},
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := d.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := d.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	isc := &iscDHCPDCollector{
		cfg: iscDHCPDConfig{
			LeasesPath: "/tmp/dhcpd.leases",
			Pools:      []iscDHCPPool{{Name: "lan", Networks: "192.168.1.0/24"}},
		},
		readFile: func(path string) ([]byte, error) {
			return []byte("lease 192.168.1.10 {\n  binding state active;\n}\nlease 10.0.0.2 {\n  binding state free;\n}\n"), nil
		},
	}
	if err := isc.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := isc.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	ov := &openvpnStatusLogCollector{
		cfg: openvpnStatusLogConfig{LogPath: "/tmp/openvpn-status.log"},
		readFile: func(path string) ([]byte, error) {
			return []byte("OpenVPN CLIENT LIST\nUpdated,now\nCommon Name,Real Address,Bytes Received,Bytes Sent,Connected Since\nclient1,1.2.3.4:1194,100,200,date\nROUTING TABLE\nEND\n"), nil
		},
	}
	if err := ov.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := ov.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestPuppetRethinkYugabyteVerneMQ(t *testing.T) {
	pp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status-service":{"state":"running","status":{"experimental":{"jvm-metrics":{"heap-memory":{"committed":1000,"used":400},"non-heap-memory":{"committed":200,"used":80},"file-descriptors":{"used":12}},"metrics":{"cpu-usage":{"used":0.2},"gc-cpu-usage":{"used":0.01}}}}}}`))
	}))
	defer pp.Close()
	p := &puppetCollector{cfg: puppetConfig{URL: pp.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := p.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	r := &rethinkdbCollector{
		cfg: rethinkdbConfig{Address: "127.0.0.1:28015", Timeout: time.Second},
		stats: func(context.Context) ([]map[string]any, error) {
			return []map[string]any{
				{"id": []any{"cluster"}},
				{"id": []any{"server", "abc"}, "server": "n1", "query_engine": map[string]any{
					"client_connections": 3.0, "clients_active": 1.0, "queries_total": 9.0, "read_docs_total": 4.0, "written_docs_total": 2.0,
				}},
			}, nil
		},
	}
	if err := r.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := r.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	yb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("yb_ysqlserver_connection_total 4\nyb_ysqlserver_max_connection_total 10\nyb_ysqlserver_active_connection_total 2\nyb_ysqlserver_connection_over_limit_total 0\nhandler_latency_yb_ysqlserver_SQLProcessor_SelectStmt_count 7\n"))
	}))
	defer yb.Close()
	y := &yugabytedbCollector{cfg: yugabytedbConfig{URL: yb.URL, Timeout: time.Second}}
	if err := y.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := y.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("vernemq_open_sockets 5\nvernemq_socket_open 20\nvernemq_socket_close 15\nvernemq_queue_message_in 8\nvernemq_queue_message_out 7\nvernemq_mqtt_publish_received 12\nvernemq_mqtt_publish_sent 11\nvernemq_bytes_received 100\nvernemq_bytes_sent 90\nvernemq_uptime 3600\n"))
	}))
	defer vm.Close()
	v := &vernemqCollector{cfg: vernemqConfig{URL: vm.URL, Timeout: time.Second}}
	if err := v.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := v.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestM12EAutoDisable(t *testing.T) {
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := (&dnsdistCollector{cfg: dnsdistConfig{URL: "http://127.0.0.1:1", Timeout: 50 * time.Millisecond}}).Init(reg); err == nil {
		t.Fatal("dnsdist should disable")
	}
	if err := (&dnsmasqDHCPCollector{
		cfg: dnsmasqDHCPConfig{LeasesPath: "/no/such/leases"},
		readFile: func(path string) ([]byte, error) {
			return nil, fmt.Errorf("missing")
		},
	}).Init(reg); err == nil {
		t.Fatal("dnsmasq_dhcp should disable")
	}
	if err := (&rethinkdbCollector{cfg: rethinkdbConfig{Address: "127.0.0.1:1", Timeout: 50 * time.Millisecond}}).Init(reg); err == nil {
		t.Fatal("rethinkdb should disable")
	}
}
