package collect

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestM9CollectorsRegistered(t *testing.T) {
	for _, name := range []string{
		"zookeeper", "nats", "varnish", "squid", "tomcat",
		"traefik", "bind", "unbound", "coredns", "hdfs",
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

func TestZooKeeperMNTR(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	mntr := "zk_avg_latency\t1\nzk_max_latency\t5\nzk_min_latency\t0\nzk_packets_received\t10\nzk_packets_sent\t9\nzk_num_alive_connections\t3\nzk_outstanding_requests\t0\nzk_server_state\tleader\nzk_znode_count\t12\nzk_watch_count\t4\nzk_ephemerals_count\t2\nzk_open_file_descriptor_count\t20\nzk_approximate_data_size\t2048\nzk_uptime\t99\n"
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 16)
				_, _ = c.Read(buf)
				_, _ = io.WriteString(c, mntr)
			}(c)
		}
	}()
	z := &zookeeperCollector{cfg: zookeeperConfig{Address: ln.Addr().String(), Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := z.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := z.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("zookeeper.requests_latency"); !ok {
		t.Fatal("missing zookeeper.requests_latency")
	}
	if _, ok := reg.Chart("zookeeper.watches"); !ok {
		t.Fatal("missing zookeeper.watches")
	}
}

func TestZooKeeperAutoDisable(t *testing.T) {
	z := &zookeeperCollector{cfg: zookeeperConfig{Address: "127.0.0.1:1", Timeout: 50 * time.Millisecond}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := z.Init(reg); err == nil {
		t.Fatal("expected Init to fail when ZooKeeper is absent")
	}
}

func TestNATSVarz(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/varz" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"connections": 4, "total_connections": 20, "in_msgs": 100, "out_msgs": 90,
			"in_bytes": 1000, "out_bytes": 800, "slow_consumers": 1, "mem": 4096, "cpu": 2.5,
		})
	}))
	defer srv.Close()
	n := &natsCollector{cfg: natsConfig{URL: srv.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := n.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := n.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestParseVarnishJSON(t *testing.T) {
	raw := `{"counters":{"MAIN.client_req":{"value":10},"MAIN.cache_hit":{"value":7},"MAIN.cache_miss":{"value":3},"MAIN.n_object":{"value":5},"MAIN.backend_req":{"value":4},"MAIN.threads":{"value":8}}}`
	m, err := parseVarnishJSON([]byte(raw))
	if err != nil || m["MAIN.client_req"] != 10 || m["MAIN.cache_hit"] != 7 {
		t.Fatalf("%v %v", m, err)
	}
	v := &varnishCollector{
		cfg: varnishConfig{Command: "varnishstat", Timeout: time.Second},
		run: func(context.Context, string, ...string) ([]byte, error) { return []byte(raw), nil },
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := v.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := v.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestParseSquidCounters(t *testing.T) {
	body := "client_http.requests = 50\nclient_http.hits = 10\nclient_http.errors = 1\nclient_http.kbytes_in = 2\nclient_http.kbytes_out = 8\nserver.all.requests = 40\nserver.all.kbytes_in = 3\nserver.all.kbytes_out = 4\n"
	m, err := parseSquidCounters(body)
	if err != nil || m["client_http.requests"] != 50 || m["server.all.requests"] != 40 {
		t.Fatalf("%v %v", m, err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	s := &squidCollector{cfg: squidConfig{URL: srv.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := s.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := s.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestParseTomcatStatus(t *testing.T) {
	xml := `<status><jvm><memory free="100" total="400" max="800"/></jvm>` +
		`<connector name='"http-nio-8080"'><threadInfo maxThreads="200" currentThreadCount="10" currentThreadsBusy="2"/>` +
		`<requestInfo maxTime="5" processingTime="20" requestCount="30" errorCount="1" bytesReceived="9" bytesSent="11"/></connector></status>`
	st, err := parseTomcatStatus([]byte(xml))
	if err != nil || st.memFree != 100 || st.requests != 30 || st.busy != 2 {
		t.Fatalf("%+v %v", st, err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(xml))
	}))
	defer srv.Close()
	tc := &tomcatCollector{cfg: tomcatConfig{URL: srv.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := tc.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := tc.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("tomcat.jvm_memory_usage"); !ok {
		t.Fatal("missing tomcat.jvm_memory_usage")
	}
	if _, ok := reg.Chart("tomcat.connector_errors"); !ok {
		t.Fatal("missing tomcat.connector_errors")
	}
}

func TestTraefikMetrics(t *testing.T) {
	body := `
# TYPE traefik_entrypoint_requests_total counter
traefik_entrypoint_requests_total{code="200",entrypoint="web"} 10
traefik_entrypoint_requests_total{code="404",entrypoint="web"} 2
traefik_entrypoint_requests_total{code="500",entrypoint="web"} 1
traefik_entrypoint_open_connections{method="GET"} 3
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	tr := &traefikCollector{cfg: traefikConfig{URL: srv.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := tr.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := tr.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestParseBindJSON(t *testing.T) {
	raw := `{"opcodes":{"QUERY":20,"UPDATE":1},"nsstats":{"QrySuccess":15,"QryNXDOMAIN":3,"QrySERVFAIL":1,"QryNxrrset":1,"QryRecursion":8},"qtypes":{"A":10,"AAAA":5,"TXT":2}}`
	st, err := parseBindJSON([]byte(raw))
	if err != nil || st.query != 20 || st.success != 15 || st.a != 10 || st.other != 2 {
		t.Fatalf("%+v %v", st, err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(raw))
	}))
	defer srv.Close()
	b := &bindCollector{cfg: bindConfig{URL: srv.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := b.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := b.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestParseUnboundStats(t *testing.T) {
	s := "total.num.queries=100\ntotal.num.cachehits=80\ntotal.num.cachemiss=20\ntotal.num.prefetch=2\ntotal.num.recursivereplies=20\ntotal.requestlist.avg=0.5\ntotal.requestlist.max=3\n"
	m := parseUnboundStats(s)
	if m["total.num.queries"] != 100 || m["total.num.cachehits"] != 80 {
		t.Fatalf("%v", m)
	}
	u := &unboundCollector{
		cfg: unboundConfig{Command: "unbound-control", Timeout: time.Second},
		run: func(context.Context, string, ...string) ([]byte, error) { return []byte(s), nil },
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := u.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := u.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestCoreDNSMetrics(t *testing.T) {
	body := `
coredns_dns_requests_total{proto="udp",type="A"} 10
coredns_dns_requests_total{proto="tcp",type="A"} 2
coredns_dns_responses_total{rcode="NOERROR"} 9
coredns_dns_responses_total{rcode="NXDOMAIN"} 3
coredns_panics_total 0
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := &corednsCollector{cfg: corednsConfig{URL: srv.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := c.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("coredns.dns_panic_count_total"); !ok {
		t.Fatal("missing coredns.dns_panic_count_total")
	}
}

func TestParseHDFSJMX(t *testing.T) {
	raw := `{"beans":[{"name":"Hadoop:service=NameNode,name=FSNamesystemState","CapacityUsed":100,"CapacityRemaining":900,"FilesTotal":12,"BlocksTotal":30,"MissingBlocks":1,"CorruptBlocks":0,"UnderReplicatedBlocks":2,"NumLiveDataNodes":3,"NumDeadDataNodes":1,"TotalLoad":4}]}`
	st, err := parseHDFSJMX([]byte(raw))
	if err != nil || st.used != 100 || st.remaining != 900 || st.files != 12 || st.live != 3 {
		t.Fatalf("%+v %v", st, err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(raw))
	}))
	defer srv.Close()
	h := &hdfsCollector{cfg: hdfsConfig{URL: srv.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := h.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := h.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPStatusClass(t *testing.T) {
	if httpStatusClass("404") != "4xx" || httpStatusClass("200") != "2xx" || httpStatusClass("x") != "other" {
		t.Fatal(httpStatusClass("404"))
	}
}
