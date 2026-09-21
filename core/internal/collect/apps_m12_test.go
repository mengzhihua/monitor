package collect

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestM12CollectorsRegistered(t *testing.T) {
	for _, name := range []string{
		"proxysql", "clickhouse", "cockroachdb", "pulsar", "envoy",
		"upsd", "zfspool", "dmcache", "filecheck", "supervisord", "monit", "snmp",
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

func TestProxySQLStatus(t *testing.T) {
	p := &proxysqlCollector{
		cfg: proxysqlConfig{Address: "127.0.0.1:6032", Timeout: time.Second},
		query: func(_ context.Context, _ string) ([][]string, error) {
			return [][]string{
				{"Client_Connections_connected", "4"},
				{"Questions", "10"},
				{"Slow_queries", "1"},
				{"Com_stmt_prepare", "2"},
			}, nil
		},
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := p.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestClickHouseHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		if strings.Contains(q, "system.metrics") {
			_, _ = w.Write([]byte("TCPConnection\t3\nHTTPConnection\t1\nQuery\t2\nMemoryTracking\t1000\n"))
			return
		}
		_, _ = w.Write([]byte("Query\t50\nSelectQuery\t40\nInsertQuery\t10\n"))
	}))
	defer srv.Close()
	c := &clickhouseCollector{cfg: clickhouseConfig{URL: srv.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := c.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestCockroachAndPulsarAndEnvoy(t *testing.T) {
	cr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("sql_conns 7\nsql_querycount 20\nliveness_livenodes 3\nsys_rss 1000\nsys_uptime 9\ncapacity_used 1\ncapacity_available 9\n"))
	}))
	defer cr.Close()
	c := &cockroachdbCollector{cfg: cockroachdbConfig{URL: cr.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := c.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	pu := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("pulsar_topics_count 4\npulsar_rate_in 10\npulsar_rate_out 8\npulsar_throughput_in 100\npulsar_throughput_out 80\npulsar_subscription_back_log 2\n"))
	}))
	defer pu.Close()
	p := &pulsarCollector{cfg: pulsarConfig{URL: pu.URL, Timeout: time.Second}}
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := p.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	en := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("envoy_server_uptime 12\nenvoy_server_memory_allocated 100\nenvoy_server_total_connections 3\nenvoy_cluster_upstream_cx_connect_fail 1\nenvoy_http_downstream_rq_xx{envoy_response_code_class=\"2xx\"} 9\nenvoy_http_downstream_rq_xx{envoy_response_code_class=\"5xx\"} 1\n"))
	}))
	defer en.Close()
	e := &envoyCollector{cfg: envoyConfig{URL: en.URL, Timeout: time.Second}}
	if err := e.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := e.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestUPSDProtocol(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 256)
				n, _ := c.Read(buf)
				cmd := string(buf[:n])
				switch {
				case strings.Contains(cmd, "LIST UPS"):
					_, _ = io.WriteString(c, "BEGIN LIST UPS\nUPS myups \"desc\"\nEND LIST UPS\n")
				case strings.Contains(cmd, "LIST VAR"):
					_, _ = io.WriteString(c, "BEGIN LIST VAR myups\nVAR myups ups.status \"OL CHRG\"\nVAR myups ups.load \"20\"\nVAR myups battery.charge \"99\"\nVAR myups battery.runtime \"1200\"\nVAR myups input.voltage \"230\"\nEND LIST VAR myups\n")
				}
			}(c)
		}
	}()
	u := &upsdCollector{cfg: upsdConfig{Address: ln.Addr().String(), Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := u.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := u.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("upsd.ups_status.myups"); !ok {
		t.Fatal("missing ups chart")
	}
}

func TestZpoolAndDMCacheParse(t *testing.T) {
	rows, err := parseZpoolList([]byte("tank\t1000\t400\t600\t5\t40\tONLINE\n"))
	if err != nil || len(rows) != 1 || rows[0].name != "tank" || rows[0].cap != 40 {
		t.Fatalf("%v %v", rows, err)
	}
	z := &zfspoolCollector{
		cfg: zfspoolConfig{Command: "zpool", Timeout: time.Second},
		run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("tank\t1000\t400\t600\t5\t40\tONLINE\n"), nil
		},
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := z.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := z.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	dm, err := parseDMCacheStatus([]byte("vg-cache: 0 1024 cache 8 10/100 512 20/200 5 1 2 3 0 0 0\n"))
	if err != nil || len(dm) != 1 || dm[0].readHits != 5 || dm[0].cacheUsed != 20 {
		t.Fatalf("%+v %v", dm, err)
	}
	d := &dmcacheCollector{
		cfg: dmcacheConfig{Command: "dmsetup", Timeout: time.Second},
		run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("vg-cache: 0 1024 cache 8 10/100 512 20/200 5 1 2 3 0 0 0\n"), nil
		},
	}
	if err := d.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := d.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestFilecheckAndSNMP(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.log")
	if err := os.WriteFile(path, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &filecheckCollector{cfg: filecheckConfig{Files: []string{path}, Dirs: []string{dir}}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := f.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := f.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	s := &snmpCollector{
		cfg: snmpConfig{Address: "127.0.0.1", Community: "public", Version: "2c", Command: "snmpwalk", Timeout: time.Second},
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			oid := args[len(args)-1]
			switch {
			case strings.HasSuffix(oid, "1.3.0"):
				return []byte(".1.3.6.1.2.1.1.3.0 = Timeticks: (12300) 0:02:03.00\n"), nil
			case strings.HasSuffix(oid, "2.2.1.2"):
				return []byte(".1.3.6.1.2.1.2.2.1.2.1 = STRING: eth0\n"), nil
			case strings.HasSuffix(oid, "2.2.1.10"):
				return []byte(".1.3.6.1.2.1.2.2.1.10.1 = Counter32: 100\n"), nil
			case strings.HasSuffix(oid, "2.2.1.16"):
				return []byte(".1.3.6.1.2.1.2.2.1.16.1 = Counter32: 50\n"), nil
			case strings.HasSuffix(oid, "2.2.1.8"):
				return []byte(".1.3.6.1.2.1.2.2.1.8.1 = INTEGER: 1\n"), nil
			}
			return nil, fmtSNMPUnknown(oid)
		},
	}
	if err := s.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := s.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("snmp.device_net.eth0"); !ok {
		t.Fatal("missing iface chart")
	}
}

func fmtSNMPUnknown(oid string) error { return &snmpUnknown{oid} }

type snmpUnknown struct{ oid string }

func (s *snmpUnknown) Error() string { return "unknown oid " + s.oid }

func TestSupervisordAndMonit(t *testing.T) {
	rpc := `<?xml version="1.0"?><methodResponse><params><param><value><array><data>
<value><struct>
<member><name>name</name><value><string>web</string></value></member>
<member><name>group</name><value><string>app</string></value></member>
<member><name>statename</name><value><string>RUNNING</string></value></member>
</struct></value>
</data></array></value></param></params></methodResponse>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(rpc))
	}))
	defer srv.Close()
	s := &supervisordCollector{cfg: supervisordConfig{URL: srv.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := s.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := s.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	xmlBody := `<?xml version="1.0"?><monit><service><name>sshd</name><status>0</status><monitor>1</monitor></service>
<service><name>nginx</name><status>1</status><monitor>1</monitor></service></monit>`
	ms := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(xmlBody))
	}))
	defer ms.Close()
	m := &monitCollector{cfg: monitConfig{URL: ms.URL, Timeout: time.Second}}
	if err := m.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := m.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("monit.services")
	if !ok {
		t.Fatal("missing monit chart")
	}
	_, v := ch.LastValues()
	if v["ok"] != 1 || v["error"] != 1 {
		t.Fatalf("%v", v)
	}
}

func TestM12AutoDisable(t *testing.T) {
	if err := (&filecheckCollector{}).Init(registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)); err == nil {
		t.Fatal("expected filecheck disable")
	}
	p := &pulsarCollector{cfg: pulsarConfig{URL: "http://127.0.0.1:1/metrics", Timeout: 50 * time.Millisecond}}
	if err := p.Init(registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)); err == nil {
		t.Fatal("expected pulsar disable")
	}
}
