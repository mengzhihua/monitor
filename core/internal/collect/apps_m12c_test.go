package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestM12ContCollectorsRegistered(t *testing.T) {
	for _, name := range []string{
		"fluentd", "logstash", "cassandra", "ceph", "couchdb", "couchbase",
		"hddtemp", "openvpn", "beanstalk", "uwsgi", "powerdns", "dnsmasq",
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

func TestFluentdAndLogstashHTTP(t *testing.T) {
	fd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"plugins":[{"plugin_id":"out1","type":"forward","plugin_category":"output","retry_count":2,"buffer_queue_length":3,"buffer_total_queued_size":40}]}`))
	}))
	defer fd.Close()
	f := &fluentdCollector{cfg: fluentdConfig{URL: fd.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := f.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := f.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	ls := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jvm":{"threads":{"count":8},"mem":{"heap_used_percent":45,"heap_committed_in_bytes":2048,"heap_used_in_bytes":1024},"uptime_in_millis":90000},"process":{"open_file_descriptors":12},"events":{"in":10,"filtered":9,"out":8}}`))
	}))
	defer ls.Close()
	l := &logstashCollector{cfg: logstashConfig{URL: ls.URL, Timeout: time.Second}}
	if err := l.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := l.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestCassandraPrometheus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`
org_apache_cassandra_metrics_clientrequest_count{name="Latency",scope="Read"} 10
org_apache_cassandra_metrics_clientrequest_count{name="Latency",scope="Write"} 8
org_apache_cassandra_metrics_clientrequest_count{name="Failures",scope="Read"} 1
org_apache_cassandra_metrics_clientrequest_count{name="Failures",scope="Write"} 2
jvm_memory_bytes_used{area="heap"} 100
jvm_memory_bytes_used{area="nonheap"} 20
org_apache_cassandra_metrics_droppedmessage_count 3
org_apache_cassandra_metrics_storage_count{name="Load"} 1000
org_apache_cassandra_metrics_compaction_value{name="PendingTasks"} 4
`))
	}))
	defer srv.Close()
	c := &cassandraCollector{cfg: cassandraConfig{URL: srv.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := c.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestCephStatusJSON(t *testing.T) {
	c := &cephCollector{
		cfg: cephConfig{Command: "ceph", Timeout: time.Second},
		run: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(`{"health":{"status":"HEALTH_WARN"},"monmap":{"num_mons":3},"osdmap":{"num_osds":10,"num_up_osds":9,"num_in_osds":8},"pgmap":{"num_pgs":64,"bytes_used":400,"bytes_avail":600,"read_bytes_sec":1,"write_bytes_sec":2,"read_op_per_sec":3,"write_op_per_sec":4}}`), nil
		},
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := c.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestCouchDBAndCouchbase(t *testing.T) {
	db := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "_stats"):
			_, _ = w.Write([]byte(`{"couchdb":{"database_reads":{"value":5},"database_writes":{"value":3},"httpd_view_reads":{"value":1},"open_os_files":{"value":7},"httpd_status_codes":{"200":{"value":9},"404":{"value":1},"500":{"value":2}}}}`))
		case strings.Contains(r.URL.Path, "_active_tasks"):
			_, _ = w.Write([]byte(`[{"type":"indexer"},{"type":"replication"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer db.Close()
	cd := &couchdbCollector{cfg: couchdbConfig{URL: db.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := cd.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := cd.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	cb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"default","basicStats":{"quotaPercentUsed":12,"opsPerSec":4,"itemCount":9,"memUsed":100,"diskUsed":200}}]`))
	}))
	defer cb.Close()
	c := &couchbaseCollector{cfg: couchbaseConfig{URL: cb.URL, Timeout: time.Second}}
	if err := c.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestHDDTempParse(t *testing.T) {
	disks, err := parseHddTemp("|/dev/sda|WDC|45|C||/dev/sdb|ERR|ERR|C|")
	if err != nil {
		t.Fatal(err)
	}
	if len(disks) != 2 || disks[0].temp != "45" {
		t.Fatalf("%+v", disks)
	}
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
			_, _ = io.WriteString(c, "|/dev/sda|WDC|41|C|")
			_ = c.Close()
		}
	}()
	h := &hddtempCollector{cfg: hddtempConfig{Address: ln.Addr().String(), Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := h.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := h.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestOpenVPNLoadStats(t *testing.T) {
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
			_, _ = io.WriteString(c, ">INFO:OpenVPN Management Interface Version 1\n")
			buf := make([]byte, 256)
			_, _ = c.Read(buf)
			_, _ = io.WriteString(c, "SUCCESS: nclients=2,bytesin=100,bytesout=200\n")
			_ = c.Close()
		}
	}()
	o := &openvpnCollector{cfg: openvpnConfig{Address: ln.Addr().String(), Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := o.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := o.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestBeanstalkAndUwsgi(t *testing.T) {
	bln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer bln.Close()
	body := "---\ncurrent-jobs-ready: 3\ncurrent-jobs-buried: 1\ncurrent-jobs-urgent: 0\ncurrent-jobs-delayed: 2\ncurrent-jobs-reserved: 1\ntotal-jobs: 9\njob-timeouts: 0\ncurrent-tubes: 1\ncurrent-connections: 2\ncurrent-producers: 1\ncurrent-workers: 1\ncurrent-waiting: 0\ntotal-connections: 5\nuptime: 10\n"
	go func() {
		for {
			c, err := bln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 32)
			_, _ = c.Read(buf)
			_, _ = io.WriteString(c, fmt.Sprintf("OK %d\r\n%s", len(body), body))
			_ = c.Close()
		}
	}()
	b := &beanstalkCollector{cfg: beanstalkConfig{Address: bln.Addr().String(), Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := b.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := b.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	uln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer uln.Close()
	go func() {
		for {
			c, err := uln.Accept()
			if err != nil {
				return
			}
			_, _ = io.WriteString(c, `{"workers":[{"id":1,"requests":10,"exceptions":1,"harakiri_count":0,"respawn_count":2,"tx":100,"status":"idle"}]}`)
			_ = c.Close()
		}
	}()
	u := &uwsgiCollector{cfg: uwsgiConfig{Address: uln.Addr().String(), Timeout: time.Second}}
	if err := u.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := u.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestPowerDNSAndDnsmasq(t *testing.T) {
	pd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"udp-queries","type":"StatisticItem","value":"10"},{"name":"tcp-queries","type":"StatisticItem","value":"2"},{"name":"udp-answers","type":"StatisticItem","value":"9"},{"name":"tcp-answers","type":"StatisticItem","value":"2"},{"name":"query-cache-hit","type":"StatisticItem","value":"5"},{"name":"query-cache-miss","type":"StatisticItem","value":"1"},{"name":"packetcache-hit","type":"StatisticItem","value":"4"},{"name":"packetcache-miss","type":"StatisticItem","value":"2"},{"name":"query-cache-size","type":"StatisticItem","value":"8"},{"name":"packetcache-size","type":"StatisticItem","value":"7"},{"name":"key-cache-size","type":"StatisticItem","value":"1"},{"name":"meta-cache-size","type":"StatisticItem","value":"1"},{"name":"latency","type":"StatisticItem","value":"30"}]`))
	}))
	defer pd.Close()
	p := &powerdnsCollector{cfg: powerdnsConfig{URL: pd.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := p.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	d := &dnsmasqCollector{
		cfg: dnsmasqConfig{Address: "127.0.0.1:1", Timeout: time.Second},
		query: func(_ context.Context, qname string) ([]string, error) {
			switch {
			case strings.HasPrefix(qname, "servers"):
				return []string{"10.0.0.1#53 4 1"}, nil
			case strings.HasPrefix(qname, "cachesize"):
				return []string{"150"}, nil
			case strings.HasPrefix(qname, "insertions"):
				return []string{"3"}, nil
			case strings.HasPrefix(qname, "evictions"):
				return []string{"1"}, nil
			case strings.HasPrefix(qname, "hits"):
				return []string{"20"}, nil
			default:
				return []string{"5"}, nil
			}
		},
	}
	if err := d.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := d.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestM12ContAutoDisable(t *testing.T) {
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := (&fluentdCollector{cfg: fluentdConfig{URL: "http://127.0.0.1:1", Timeout: 50 * time.Millisecond}}).Init(reg); err == nil {
		t.Fatal("fluentd should disable")
	}
	if err := (&dnsmasqCollector{cfg: dnsmasqConfig{Address: "127.0.0.1:1", Timeout: 50 * time.Millisecond}}).Init(reg); err == nil {
		t.Fatal("dnsmasq should disable")
	}
}

func TestJSONHelpers(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(`{"a":{"b":3,"s":"ok"}}`), &m); err != nil {
		t.Fatal(err)
	}
	if nestFloat(m, "a", "b") != 3 || nestString(m, "a", "s") != "ok" {
		t.Fatalf("%v", m)
	}
}
